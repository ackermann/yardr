# Integrations

An integration is a service outside the yard that the yard hears and, where
the service allows it, speaks in: Linear and GitHub, each a pack of kind
source, and a service a file declares, such as Pushover. This page says what one is
made of today, how one becomes part of a yard, with Linear as the example
from nothing to a merged bead closing its issue, and what someone would
write to add a service. It ends with what is missing.

It describes what is built. The other pages go deeper and are linked where
they do: [subscriptions.md](subscriptions.md), [links.md](links.md),
[crew.md](crew.md), [packs.md](packs.md),
[configuration.md](configuration.md).

## What an integration is

Two things, kept in two places:

- **An adapter**: Go code in `internal/hooks`, compiled into yardr. One
  adapter serves every **declared source**: a service added by one YAML
  file and no Go ([below](#a-declared-source)), which today can do all
  eleven things of this list, each with the keys its row names. Linear
  and GitHub are two: the `linear` and `github` packs each bring a
  declaration ([packs.md](packs.md)). The name is the **source**. What is
  still an adapter of its own is `token`, yardr's own, which receives and
  nothing else. A declared source cannot take that name, so the generic
  webhook pack, for Zapier, Make, n8n and the like, is the source
  `webhook`: it receives like `token`, checked by a shared secret in a
  header, and it can also send.
- **Settings in the yard**: a hook (the endpoint and its secret), a
  crew member's credential, identity and grants, subscriptions, links, a stage
  map. They live in the store and in files in the yard's home, and are set
  with the commands of [part 2](#how-one-becomes-part-of-a-yard).

The adapter is a set of capabilities. Each is a Go interface, and a source
has a capability exactly when its adapter implements the interface: the
code asks with a type assertion (`hooks.ReplierFor`, `OpenerFor`,
`StaterFor`, `ForwarderFor`, `LinkSources`) and refuses the command for a
source that lacks it, naming those that have it (`yardr cannot open threads
in token (it can in github, linear)`). Only the first is required.

| capability | interface (file) | `token` | a declared source (Linear, GitHub) | `webhook` (a pack) |
|---|---|---|---|---|
| receive | `Adapter` (`hooks.go`) | yes | yes, with a `hook` key in its file | yes, `X-Yardr-Secret` in a header |
| identify | none: read from the envelope by the store | never | as its envelope says | as its envelope says |
| link | `Linker` (`issue.go`) | no | yes, with a `link` key in its file | no |
| reply, read a thread | `Replier` (`reply.go`) | no | yes, with `thread.reply`, `thread.posted` and `thread.read` in its file | no |
| open a thread | `Opener` (`open.go`) | no | yes, with `thread.open`, `thread.opened` and `thread.place` in its file, and the reply keys | no |
| set a thread's status | `Stater` (`state.go`) | no | yes, with `thread.state` and `thread.states` in its file, and the reply keys | no |
| move with a bead's stage | `Forwarder` (`forward.go`) | no | yes, with the status keys and a `link` key in its file | no |
| tell a user outside the yard | `Notifier` (`notify.go`) | no | yes, with a `notify` key in its file | no |
| say what a harness kind's subscription still carries | none: a command the source's file names, run by `internal/limits` | no | yes, with a `limits` key in its file | no |
| be read by a session or a crew member | none: a read call of the source's file, made by the server (`store.CallSource`) | no | yes, for whoever the operator granted it ([below](#a-call-by-a-session-or-a-crew-member)) | no, it has no read call |
| be updated by a session or a crew member | none: an update call of the source's file, made the same way | no | yes, for whoever the operator granted that call by its name ([below](#an-update)) | yes, `send`, to the URL that is its credential |

The interfaces build on each other: an `Opener` and a `Stater` are each a
`Replier`, and a `Forwarder` is a `Stater`. `token` is the smallest
integration there is: it receives and nothing else.

One source has no adapter and no file: `peer`, the yards this one is linked
with, heard about the beads this yard has away at them
([peers.md](peers.md#the-source-peer)). It is yardr's own, as `yardr` is,
and has three of the capabilities, each in the store and not in
`internal/hooks`:

| capability | how `peer` has it |
|---|---|
| receive | a `bead-event` message taken over a link is one delivery, with an envelope the yard makes itself (`store.SourcePeer`, `peersource.go`). No hook, no secret and no table: the link's signature verifies it |
| identify | never: identities are not merged across yards. `involved` is following the bead |
| link | a row read from the record of the pair (`at`, `was at`, `from`), never stored, which `link add`, `rm` and `move` refuse |
| read a thread | `yardr thread show <bead>`, or `<seq>` of a delivery: the notes of the other yard's bead, asked for over the link with a `bead-read` message and its answer, no credential (`store.PeerRead`, `peerread.go`; [peers.md](peers.md#reading-the-other-yards-bead)) |
| reply | `yardr reply <bead>`, or `<seq>` of a delivery: one note on the other yard's bead, queued for it as a `bead-note` message, no credential (`store.PeerNote`, `peernote.go`; [peers.md](peers.md#answering-on-the-other-yards-bead)) |
| every other | no: it opens no thread, sets no status and maps no stage |

### Receive

*What for.* A delivery from the service becomes one `hook` event in the
yard's event log, in a shape that is the same for every source.

*The interface.*

```go
type Adapter interface {
    Verify(r *http.Request, body, secret []byte, now time.Time) (delivery string, err error)
    Normalize(h http.Header, body []byte) (*Envelope, error)
    Bodies() BodyScope
}
```

- `Verify` proves the delivery came from the service, from the request's
  headers, the raw body and the hook's secret, and returns the service's id
  of the delivery (`""` when it has none). A declared source is verified
  from what its file says under `hook.verify`: an HMAC (SHA-256, or the
  SHA-1 or SHA-512 that `algorithm` names) of the body,
  or of a template over its timestamp, its delivery id and its body, in
  the header it names (`Linear-Signature`, `X-Hub-Signature-256`), and
  after it the timestamp where it says one is; Linear's signed
  `webhookTimestamp` must also be within a minute of the yard's clock.
  `token` compares a bearer token. ([below](#what-the-service-sends-in)).
- `Normalize` turns the verified body into the envelope. A declared
  source's is a command of its file, run with the verified body and no
  credential.
- `Bodies` says how far a body identifies a delivery, which decides the
  dedupe keys below. A declared source says `unique` or `repeat`.

*The envelope.* What routing reads, whatever the source:

| field | what it is |
|---|---|
| `source` | the source's name |
| `type`, `action` | what happened to what: `issue` `create`, `comment` `update` in Linear; the event and its action in GitHub (`issue_comment` `created`) |
| `actor` | who did it: `id` and `name`. The id is what an identity is matched against |
| `thread` | what the delivery belongs to, as `id`, `title`, `url`: the issue a comment is on, or the issue itself. Empty for what belongs to nothing (a `token` message). GitHub gives a CI run on no pull request the thread of its branch, which is no issue (see GitHub) |
| `mentions` | the ids or keys of the people the text mentions |
| `assignee`, `delegate` | who the issue is assigned to, and (Linear) the agent it is handed to while a person stays the assignee |
| `text` | the start of the delivery's own words, whitespace collapsed, at most 500 characters |
| `url` | the link to what the delivery is |
| `delivery` | the service's id of the delivery |
| `message`, `parent` | the id of the comment the delivery is, and of the comment it answers, so a reply lands in the same conversation |
| `entity` | the id of what the text belongs to when the service sends that text again with every change (an issue's description, an edited comment): the store counts only the mentions the entity did not have before, and the event carries them as `new_mentions` |
| `fields` | what the source says about where the delivery belongs, each a list of values: `team` (key and name), `project`, `label`, `sender` in Linear, with `speaker` derived by the yard for a delivery it heard by identity; `repo`, `label`, `number`, `state`, `conclusion`, `head_branch` and more in GitHub. Any source's normalize may set `outsider` to `yes` on a delivery whose text was written by someone who is none of the source's own people: the yard then marks its inbox line as information, not instructions, and leaves its text out of the notes it writes |

`fields` are there for subscriptions: `--where team=YRD` matches a delivery
whose `team` field has the value `YRD`. `--where type=…` and `action=…` read
the envelope's own two fields. A where key that a source's deliveries never
carry matches nothing. A declared source's events carry the keys its file
names under `hook.keys`, which a subscription's `--where` may then name
beside `type`, `action`, `team`, `project`, `repo` and `label`; any other
key is refused when the subscription is made.

The event is the envelope plus `hook` (the hook's name), `deliveries` (the
crew members it reached, each with the reason), `notified` (the users) and
`new_mentions`.

*What is kept.* The event keeps the envelope, for as long as events are
kept (90 days unless `YARDR_EVENT_RETENTION` says otherwise). The raw body,
up to 1 MiB, is kept beside it under the event's seq for `yardr hook show
<seq>`, for 30 days (`YARDR_HOOK_PAYLOAD_RETENTION`). A larger body is
refused with 413
([configuration.md](configuration.md#retention-keeping-yarddb-bounded)).
The delivery's dedupe keys (below) go with the event, but not while a replay
of the delivery could still pass a source's timestamp window: a key is kept
for twice the longest `hook.verify.timestamp.window` any declared source
takes, and 7 days more, however short the retention of events is set.

*Dedupe.* A delivery is recorded at most once. Its keys are checked after
`Verify`, in the write that records it:

| `bodies` | who says it | keys |
|---|---|---|
| `unique` | `linear` | the body's sha256, per source; the delivery id, per hook |
| `unique-per-hook` | `github` | the body's sha256, per hook; the delivery id, per hook |
| `repeat` | `token` | the delivery id (`Yard-Delivery`), per hook |

A declared source says one of the three under `hook.bodies`: `unique`
dedupes a body once per source, `unique-per-hook` once per hook (one
event's body goes to every webhook on a GitHub repository), `repeat` on
the delivery id in the header its file names alone. A body that may come
again is told from a resend by that id and nothing else, so a delivery of
a `repeat` source that names none is refused with 400 (`no_delivery`): a
`token` sender sets `Yard-Delivery` on every delivery, a new id for each
one it means, and the same id again for a resend.

A duplicate is answered 200 and leaves no event
([subscriptions.md](subscriptions.md#redeliveries)).

*A refused delivery* is answered with its status and logged as a
`hook_rejected` event with the hook's name, the reason (`unauthorized`,
`bad_request`, `no_delivery`, `too_large`, `blocked`, `secret`,
`unknown_hook` among them) and a count, at most one per name and reason in
ten minutes
([subscriptions.md](subscriptions.md#a-refused-delivery)).

*Where a user sees it.* `yardr hook add`, `hook list`, `hook show <name>`,
`hook show <seq>` (the raw body), `yardr events`, `yardr doctor`'s hooks
lines.

### Identify

*What for.* To know which crew member a delivery is about, and which
deliveries are the crew member's own.

*No interface.* Only four fields of the envelope matter for it:
`actor.id`, `mentions`, `assignee` and `delegate`. The store compares them
with the identity each crew member has in the source:

```sh
yardr crew identity yardmaster --source linear --id <user id> --key <profile key>
```

An identity is an `id`, a `key`, or both. An id matches exactly; a key is a
name people type and matches in any case. Linear reports a mention
as a user id when the payload carries the rich text, and as the
profile key (`@yardr`) when it carries markdown only, so a Linear identity
wants both. GitHub's reports logins in lowercase, so a GitHub identity is
`--key <login>`; a GitHub App's is `--key '<slug>[bot]'`, and `@<slug>`
mentions it. Saying who a crew member is in a source is the operator's,
and a crew member that needs its identity set proposes the command with a
run ask (`yardr bead ask <id> "<question>" --run "crew identity <name>
--source <source> --id <id> --key <key>"`,
[chat.md](chat.md#questions-for-you)): the card says which crew member and
which source, and that mentions and assignments of the id or key there
will be addressed to it. A source the yard declares not, and a crew
member the yard has not, are refused.

With that, for each crew member subscribed to the source:

- a delivery whose `actor.id` is the crew member's identity never reaches it.
  It makes the crew member a **participant** in the delivery's thread;
- **mentioned**: its identity is among the new mentions;
- **assigned**: it is the `assignee` or the `delegate`;
- **participant**: it takes part in the thread. A crew member takes part in a
  thread once it acted there (above), answered there (`yardr reply`),
  opened it (`yardr thread open`) or follows it (`yardr thread follow`).
  `yardr thread list <crew member>` shows them, each with how.

**Involved** is the subscription scope that puts these together:
`involved` delivers what mentions the crew member, what is assigned or
delegated to it and what happens in a thread it takes part in. `mentions`
delivers the first only, `all` everything from the source
([subscriptions.md](subscriptions.md#scopes)). A crew member without an
identity in the source is never mentioned or assigned, and hears its own
actions as someone else's.

A user ([users.md](users.md)) holds an identity in a source too: whom
`--assign` assigns, and, in a source that hears by identity, who spoke. A
delivery from their id is kept with them as its speaker, and a
subscription there may filter `speaker=<user>`. Only `--scope all`
reaches a person, with or without the filter.

*Where a user sees it.* `yardr crew identity`, `yardr subscribe`,
`yardr crew show` (identities and subscriptions), `yardr thread list`,
`thread follow`, `thread unfollow`, and the reason on each line of `yardr
inbox`.

### Link

*What for.* A bead records the issue it belongs to, and one issue is one
link however its URL was written.

*The interface.*

```go
type Linker interface {
    IssueHost() string
    Issue(u *url.URL) (string, error)
}
```

`IssueHost` is the host the source's issue URLs are on (`linear.app`,
`github.com`); `Issue` turns a URL on it into the issue's one fixed form
and refuses what is no issue of the source (a Linear project, a GitHub pull
request). The call is made from the URL alone, without asking the service.
A declared source says the three as data under `link`: the host, an
expression that reads an issue from a URL's path, and two templates over
its groups, the fixed form and the id a thread command takes for the issue
([below](#commands-a-file-names)). A URL on a host a built-in source has
issues on is the built-in's, unless the declared source has a hook in the
yard and the built-in has none: then it is the declared's, and `link add
--source` says which either way. By a delivery's thread each is its own.

*What the yard does with it.* `yardr link add` stores the fixed form, and a
delivery's thread is matched to a link by it. A link is `tracks` (at most
one tracking bead per issue, at most one tracked issue per source on a
bead) or `relates` ([links.md](links.md)).

*Where a user sees it.* `yardr link add`, `link list`, `link move`, `link
rm`, `yardr bead create --tracks` and `--relates`, `yardr bead show`.

### Reply, open a thread, set a thread's status

*What for.* A crew member speaks in the service as itself: it answers a
delivery, starts an issue, moves an issue's status.

*The interfaces.*

```go
type Replier interface {
    Credential(ctx context.Context) (string, error)
    Reply(ctx context.Context, env Envelope, to, body, key, cred string) (Message, error)
    Posted(ctx context.Context, env Envelope, key, cred string) (*Message, error)
    Thread(ctx context.Context, env Envelope, cred string) ([]Message, error)
}

type Opener interface {
    Replier
    Place() string
    Open(ctx context.Context, in, title, body, key, cred string) (Thread, error)
    Opened(ctx context.Context, in, key, cred string, since time.Time) (*Thread, error)
}

type Stater interface {
    Replier
    SetState(ctx context.Context, thread, state, cred string) (Stated, error)
}
```

- `Credential` reads the caller's credential for the source from the
  environment of the calling process, under the variable its file names:
  `LINEAR_API_KEY`; for GitHub a GitHub App's `GITHUB_APP_ID` and
  `GITHUB_APP_PRIVATE_KEY_FILE`, else `GITHUB_TOKEN`. Where the mint's
  names are set the call runs with a token the operator's mint command
  made from them; one of them alone is refused
  ([below](#a-declared-source)).
- `Reply` posts in the thread of a delivery; `Thread` lists the thread's
  messages. `Open` starts a thread in a place (`Place` names it: a Linear
  team, a GitHub repository), assigned to a person where the caller names
  one and `Assignee` says by which part of their identity the source takes
  them (`id` or `key`; none for a source whose open assigns nobody).
  `SetState` moves a thread to what `started`,
  `review` or `done` means in the source.
- `Posted` and `Opened` find what an earlier attempt left. Every reply and
  every opened thread is on record in the store with a key before anything
  is sent, and the key goes with the post (as the comment's or issue's own
  id in Linear, as an HTML comment in the body on GitHub), so a command run
  again after a timeout does not post twice.
- A declared source answers each method with the `thread` command of the
  same name ([below](#commands-a-file-names)): the method's arguments as
  the object in, its result from the object out. The reply's parent is the
  root of the conversation it answers, found by listing the thread where
  the envelope does not carry it. `thread.state` picks the source's status
  for the word. Setting moves exactly: only a thread already
  in the status is reported as not moved. A bead's stage
  map moves by the same command with `forward`, which never moves a thread
  backwards: a thread at or past the status is reported as not moved. What
  a moved thread was in the command reports as `was`, which it may leave
  out, and the line then has no "(was …)". A source without a key lacks
  that interface, as an adapter in Go may.

*What the yard does with it.* For these three the server does not speak and
holds no credential (it does for a call of a declared source,
[below](#a-call-by-a-session-or-a-crew-member)). The CLI in the crew
member's pane makes the call, with what the
crew member's `env_file` or `env_command` put in the pane's environment
([crew.md](crew.md#outbound-credentials)). Only the crew member's
own session or the operator may speak as a crew member. Replying needs no
grant. Opening a thread needs `open_threads=<source>` on the crew member, and
setting a status needs `set_state=<source>` and a thread the crew member takes
part in; only the operator may grant either. After each, the crew member takes
part in the thread and an event lands on its journal (`replied`,
`thread_opened`, `thread_state`).

`yardr thread open <source> --in <place> --assign <user> -` (the title on
stdin) opens the thread assigned to a user of the yard, as who they are in
the source: `yardr user identity <user> --source <source> --id … --key …`
([users.md](users.md#the-record)). The source says which part of that it
names an assignee by: Linear the id, GitHub the key, a login. A user who
has none there is refused with the command that sets it, a name that is
no user is refused, and so is a source whose open takes no assignee,
naming those that do; each before anything is on record. A thread that is
already open is not assigned by running the command again.

*Where a user sees it.* `yardr reply <seq>`, `yardr thread show <seq>`,
`yardr thread open <source> --in <place> [--assign <user>]`, `yardr user
identity <user>`, `yardr thread state <seq>
<state>`, `yardr crew set <name> open_threads=… set_state=…`, `yardr
crew show` ([crew.md](crew.md#speaking-in-sources)).

### Move

*What for.* A bead's stage moves the issue it tracks, with no agent taking
part.

*The interface.*

```go
type Forwarder interface {
    Stater
    IssueThread(fixed string) (string, error)
    CredentialFrom(get func(name string) string) (string, error)
    Places(state string) error
    Forward(ctx context.Context, thread, state, cred string) (Forwarded, error)
}
```

- `IssueThread` names the issue for `Forward` from its fixed URL, since a
  link made from a URL has no thread id.
- `CredentialFrom` is `Credential` for a caller that is not the crew member's
  pane: it reads the names from what the crew member's `env_file` sets.
- `Places` says whether the source has a status for a state at all, so a
  map that names one it lacks is refused when it is written (`review` on
  GitHub).
- `Forward` is `SetState` that only moves an issue further than it stands.
- For a declared source `Places` is `thread.states`, `IssueThread` reads
  the `link` (the id a thread command takes for the fixed URL), and
  `Forward` is the one `thread.state` command, which never moves
  backwards itself.

*What the yard does with it.* A hook carries a map from `<bead
type>:<stage>` to a state and the crew member it acts as. `yardr hook list`
shows it in its `MOVES ISSUES` column. When a bead with a `tracks` link to
an issue of the hook's source enters a mapped stage, the move is written
with the bead's event and made by the daemon afterwards, with the
credential in that crew member's `env_file`, and only when the crew member has
`set_state` for the source. It is retried when the service fails, and ends
in a `thread_state` or `thread_state_skipped` event on the bead
([links.md](links.md#a-beads-stage-moves-the-issue)).

*Where a user sees it.* `yardr hook add --as <crew member> --map
<type>:<stage>=<state>`, `yardr hook list`, `yardr hook show <name>` (the
map and the moves still to make), `yardr events --bead <id>`.

### Tell a user outside the yard

A crew member is nudged in its pane. A person sees mail and notifications
when they look (`yardr inbox`, `yardr chat`), and is told on a phone when
they name a source that can tell them. Two packs bring one each: `telegram`
and `pushover`, a message through Telegram's Bot API and a push through
Pushover. Each applies in one command, and its two secrets are given to
the yard by two more, typed unseen and written to no file by hand:

```sh
yardr pack apply telegram --notify mckean=telegram
yardr source key telegram                     # the bot's token: the yard's, the operator's to give
yardr user key mckean telegram                # the chat id: the person's own, theirs or the operator's to give
```

A crew member then asks for the test message on a card, and approving
it sends one real test message to the chat:

```sh
yardr bead ask <bead> "May the yard send the test message?" --run "source check telegram --user mckean"
```

`yardr pack show telegram` (or `pushover`) has the setup notes: what to
create at the service, how each secret is given (by these commands, or on
a card a crew member asks with), and how to test.
[pushover.md](pushover.md) sets Pushover up from nothing, with the
declaration written out for another service to follow.

Telegram hears back too: the pack declares a `hook` and a `hear` key
with `from: operator`, so the chats of users whose role is `operator` are
heard as threads, answered with `yardr reply`; a member told through
telegram is not heard back. Pushover stays one-way. The pack's
setup notes walk the two-way setup; [below](#what-the-service-sends-in)
says what `hear` means for any source.

It is a capability of a source like the others, and a user names the
**source**: there is no separate thing called a notifier. A declared source
has it when its file has a `notify` key, which says which of its calls
tells a person and which of the call's parameters take the four values the
yard has for it: where the person is reached, a title, a text and a link.
`token` cannot tell, so no user names it, and the refusal lists the sources
that can.

Everything that decides whether and when a person is told ends at those
four values; sending them is one declared call. What is told, when, and
what leaves the machine is in
[users.md](users.md#told-outside-the-yard).

A message with a picture goes through the file's `image` command instead:
in `{address, title, text, image}` with the picture's local path, out
`{status}`. A call sends JSON, which carries no picture, so the command
reads the file and sends it with the words. One without a picture, a
file without the key, and a command that fails, answers no status or
answers one that is no success, go through the call, unadorned: the words
are not lost with the picture, and the daemon's log says why. Which messages
carry a picture is in [users.md](users.md#told-outside-the-yard) with the
rest of what leaves the machine.

Two packs name one. Telegram's sends the picture as a photo with the words
for its caption. Pushover's sends it as the message's one `attachment`, a
part of the multipart form the service takes beside the title and the
message, with a file name and a content type; a message takes a picture of
5,242,880 bytes at most and the service leaves making one fit to the
sender, so the command refuses a larger one itself, with its size, before
it sends anything, and the message is told as words alone. Neither
command is given the token or the person's key in an argument: curl
reads them from its stdin.

### Say what a subscription still carries

An agent runs on a subscription with a usage limit: so much in five hours,
so much in a week. When a harness stops on it, its pane says so and the
yard raises `session_harness_error` with the reason `rate_limited`
([groups.md](groups.md)); which window it was and when it opens again, the
pane does not reliably say. A source of the yard can:

```sh
yardr limits kimi
# kimi  aiquokka says (provider Kimi):
#   5h      100% used (100 of 100)  resets 00:02 (in 42m)             used up
#   Weekly  38% used (38 of 100)    resets Sat 10 Oct 09:02 (in 3d 9h)
#   an agent of kind kimi waits for the 5h window, which resets 00:02 (in 42m)
```

Without a kind it lists by provider: what the yard runs on is resolved to
its providers, each asked once, naming who burns it. `--json` prints one
entry per provider, with the burning kinds and users beside the report.

```sh
yardr limits
# kimi  aiquokka says (provider Kimi): burned by groups builders, nightshift (kind kimi)
#   5h      100% used (100 of 100)  resets 00:02 (in 42m)             used up
#   an agent burning kimi waits for the 5h window, which resets 00:02 (in 42m)
```

A source with no kinds answers for itself instead, and `yardr limits`
lists it beside the providers, each with its windows. One such source is
built into every yard: `machine`, the load, swap, disk and memory of the
machine the yard runs on, answered with no `source add`:

```sh
yardr limits machine
# machine says (provider machine):
#   load  12.5% used  reset not known
#   swap  10% used    reset not known
#   disk  40% used    reset not known
#   mem   55% used    reset not known
#   no window is used up
```

yardr holds no logic of any provider. The machine is the exception: the
yard's own body, not a provider, answered in process with no file. A
declared source has the capability
when its file has a `limits` key: **a command**, and the harness kinds it
answers for, each with the provider's own name for it. Such a file needs no
`base` and no `calls`. This one is whole, for
[aiquokka](https://github.com/McKean/aiquokka), which reads the limits of
Claude, Codex, Kimi and others from the logins their CLIs store. yardr
carries it, so it is added by its name, with no file to hand:

```yaml
# Aiquokka as a declared source: the usage limits of the subscriptions the
# agents' CLIs are logged in to, read from the logins their CLIs store. The
# source is its limits command and has no call: `yardr limits <kind>` asks
# it, and a route's condition gates on the windows it reports. The program
# is the file of the same name beside this one, which runs the aiquokka the
# yard's machine carries on PATH; the binary is the person's to install
# (yardr docs integrations). It names no login of its own: the command runs
# with the asking group's env, so a group on its own login gates on its own
# windows.
name: aiquokka
about: Usage limits of the subscriptions the agents' CLIs are logged in to (github.com/McKean/aiquokka).
limits:
  command: [aiquokka, "{provider}", --json]
  kinds:              # harness kind -> the provider's own name for it
    claude: claude
    codex: codex
    grok: grok
    kimi: kimi
    muse: muse
```

```sh
yardr source add aiquokka       # a file of that name in the directory is added instead, and the add says so
yardr source check aiquokka     # asks for each kind the file lists
yardr limits                    # every provider the yard burns, each once, naming who burns it
yardr limits kimi --json        # the report, for a program
```

What yardr carries is the declaration and the file its command names,
`aiquokka`: a shell script of one line that runs the aiquokka found on PATH.
`yardr source add` copies it into the yard's source directory, as it does any
file a command names beside a declaration ([below](#commands-a-file-names)).
The program itself is not carried. It is the operator's to install, and the
source reads nothing until it is:

1. **Install aiquokka** from its releases (github.com/McKean/aiquokka/releases):
   download the one for the platform and put its `aiquokka` on PATH; where the
   page has none for it, build it with `go install
   github.com/McKean/aiquokka@latest`. It must be found on the PATH of whoever
   runs the yard, the daemon included, and the daemon's PATH need not be the
   shell's: a binary under `~/go/bin` wants that directory on the daemon's
   PATH too. Where `aiquokka` is on PATH and the yard declares no source of
   that name, `yardr doctor` proposes the add; `yardr setup` lists it as an
   optional step, which the setup does not wait for.
2. **Check it**, with each CLI whose limits the yard should read logged in as
   that CLI logs in: `aiquokka claude --json` prints one JSON object with the
   provider's windows. A provider whose CLI is not logged in answers nothing,
   and the yard reads no windows for it until it is.
3. **Add it.** The operator adds it in a terminal with `yardr source add
   aiquokka`. A crew member proposes it, and the operator approves its card,
   which is weighty since the file is a program the yard will run:
   `yardr bead ask <bead> "Add the aiquokka limits source?" --context "Routes
   can then gate builders on token use." --run "source add aiquokka"`. The
   card shows the declaration and the command file whole, as they will be
   added; the operator approves it in footplate, or in a terminal on the
   yard's machine with `yardr bead answer <bead> <ask> --approve`. A source
   named aiquokka that the yard already has is replaced by the operator in a
   terminal, never by a card. The file lists five kinds, and `yardr source
   check aiquokka` fails for one whose CLI is not logged in on this machine,
   which only means that kind is not read. To read fewer, delete their lines
   under `kinds` in a copy of the file (the command file goes beside it) and
   add the copy.
4. **Read the windows and gate a route on them.** `yardr limits claude` prints
   the windows with the labels a condition names, and a route gates on one
   with `yardr route add --stage new --depot web --group 'builders@Weekly<90'`
   ([groups.md](groups.md#a-route-with-further-groups-and-quota-conditions)).
   The label is matched exactly, so read it from the report.

The source names no login of its own: the command runs with the env of
whoever asks, so a group on its own login gates on its own windows. Where the
muse login the yard should read lives in a file of its own, set
`MUSE_AUTH_PATH` to it in the env of the group that burns muse, in that
group's env_file (`yardr group set <group> env_file=<file>`); `yardr limits
--group <group> muse` reads the same.

With no kinds the file is shorter, and the command takes no `{provider}`:

```yaml
name: queue
limits:
  command: [queue/limits]
```

```sh
yardr source add queue.yaml
yardr source check queue       # asks the source once, for itself
yardr limits                   # its windows beside the providers'
```

The yard's own machine needs no file: `machine` is built in, answered in
process on macOS and Linux, with no credential and no thresholds of its
own. `load` is the 1-minute load average over the core count, as a percent
(10 cores at load 8 = 80), and may read past 100; `swap` is used over
total swap, or 0 where the machine has none; `disk` is used over the
volume that holds the yard's home; `mem` is used RAM over total, as a
percent. Available memory is `MemAvailable` on Linux, free plus inactive
pages on macOS. None carries a target and none resets: the thresholds live
in the route conditions that name one, as `machine.load<80`
([groups.md](groups.md#a-route-with-further-groups-and-quota-conditions)).
A condition that names `target` on such a window reads as a window that
reports none does: it holds, and the `claimed` event says the limits were
not known. A declared source named `machine` wins over the built-in while
it stands, so an operator can still replace it; removing it brings the
built-in back. Where the machine cannot be read, its limits are not known,
as for a provider that gives no answer: the yard reads `sysctl`, `vm_stat`
and `df` on macOS, `/proc/loadavg`, `/proc/meminfo` and `df` on Linux,
and answers nothing anywhere else.

Leave out of `kinds` the harnesses that are not logged in on the machine:
they have no answer, and `source check` fails for them.

- **The question** is a provider: a group's or a crew member's `provider`
  setting where one is set
  ([groups.md](groups.md#changing-a-group)), else the kinds table's mapping
  of its `kind`. Not a model: the command is asked for a provider, though a
  window of its answer may say that it counts for one model alone (below).
  A provider no source names has no limits known, and a kind that resolves
  to nothing is not asked. `yardr limits <kind>` still asks by kind, for the
  one named.
  For a source with no kinds the question is the source itself, asked by
  its name: `yardr limits machine`.
- **The command** is run by yardr itself, as the user yardr runs as:
  without a shell (each word is one element of the list), its stdin closed,
  in the yard's home, with the environment of whoever asks and nothing
  added, `{provider}` replaced by the provider's name: the `provider`
  setting's where one is set, else the name the kind maps to. A command
  that answers for its source is run as the file has it, with nothing
  replaced. It has 30
  seconds; then it and everything it started are killed as one process
  group. Name the program by its whole path (the daemon's `PATH` need not
  be your shell's), or as a file beside the declaration, which `source add`
  copies into the yard ([below](#commands-a-file-names)).
- **Its answer** is exit status 0 and one JSON object on stdout, at most
  64 KB: `provider` (a string) and `windows` (a list), each window with a
  `label` (a string) and, where the provider knows them, `used_percent` (a
  number), `target_percent` (a number from 0 to 100: the share of the
  window that has passed), `resets_at` (a time as RFC 3339 writes one),
  `used` and `limit` (whole numbers), `model` (a string: the model the
  window counts for alone, in the provider's words, for a provider that caps
  one model beside what its models share; left out of a window they all draw
  from); `plan` (a string) beside them. Other keys are passed over. A
  `resets_at` before 2020 is read as not known: it is how a time that was
  never set gets printed.
- **What the yard reads from it itself** is when a window is used up
  (`used_percent` 100 or more, or `used` at a `limit` above 0) and when it
  is burned past its target, which `yardr limits` marks over target. Of
  several that are used up, `yardr limits` shows the one that resets last
  as the window an agent waits for (`window` and `resets_at` in `--json`).
  It reads `model` for two things: a window that has one is a per-model
  window, which `yardr limits` marks (`model Fable`), which holds no group
  that names no condition
  ([groups.md](groups.md#a-route-with-further-groups-and-quota-conditions)),
  and which an agent that stopped on a usage limit is told it waits for
  only when no other window is used up
  ([groups.md](groups.md#a-usage-limit-says-its-window-and-when-it-resets)).
  It does not match a window's model to a group's or an agent's: a
  condition that names the window reads it for whichever group it is on.
- **Anything else is no answer**: another exit status, nothing within the
  30 seconds, output that is not that object. The limits are then *not
  known*, with a reason in one line that ends with the last lines the
  command printed on stderr, never what it printed on stdout. `yardr limits`
  prints it and exits 1; `--json` has it as `limits_unknown`.
- **Whose subscription.** With `yardr limits --group <group>` the command
  runs with that group's env, and the answer is for the login the group
  burns; the daemon's quota and harness-error reads run under the judged
  group's env the same way. Otherwise the answer is for the login the
  provider reads on this machine, the operator's: a group whose env gives
  its harness another account is not described by it, nor is a crew member
  on another login.
- **When it is asked.** A provider is slow next to everything else here,
  may itself be bounded in how often it answers, and may renew a stored
  login as it reads one (aiquokka does, and writes the new one back where
  the CLI keeps it, which is why 30 seconds are never cut short). So the
  command is run in seven places and no other. Three are **a person
  asking**: once per provider by `yardr limits`, once per listed kind by
  `yardr source check <name>`, and once per provider in use by `yardr doctor
  --try`; none of them remembers an answer. The fourth is the daemon, **when a
  harness has stopped on a usage limit**: while it raises a
  `session_harness_error` of reason `rate_limited`, for a session or a crew
  member, it asks for that harness's kind — under the session's group's env
  for a session — and keeps what the provider said for 60 seconds in its
  memory, by provider and login, so agents that stop together on one login
  are one run
  ([groups.md](groups.md#a-usage-limit-says-its-window-and-when-it-resets)).
  The fifth is the daemon again, **at the reset that event told of**: for
  an agent whose window was to reset within six hours it asks once more,
  a minute after the reset and just before it prompts the agent, with the
  same memory
  ([groups.md](groups.md#a-window-that-resets-within-six-hours-is-waited-for)).
  The sixth is the dispatcher, **for a ready bead whose route puts a quota
  condition on a group or names a group that starts agents**, which starts
  no session while a window of its provider is used up, condition or none
  (a group with none reads no per-model window;
  [groups.md](groups.md#a-route-with-further-groups-and-quota-conditions)):
  at the moment one of the route's groups could start the bead it asks for
  the group's provider under the group's env, and for each source the
  condition names for the source itself, and for this it keeps what each
  said for five minutes, a failed run included. So that is at most twelve
  runs an hour for a provider read under one group's env, or for a source,
  however many beads are ready, none while no ready
  bead meets such a route, and a pass that asks holds for at most the
  command's 30 seconds.
  The seventh is the server, **for the web view at /limits**
  ([web.md](web.md#the-lists)): a GET asks for each provider in use
  whose answer is older than a minute, and reads in between share the
  last answer, concurrent reads the one run on its way. So that is at
  most one run a minute per provider however often the route is read.
  Nothing else asks: not a dispatcher pass that finds nothing stopped on a
  limit and no ready bead at such a route, not the start of a session on a
  route with no condition to a group that starts no agents, not a timer, not
  a view that names a route's groups, and not the hold that keeps a
  group from starting a further session with a model while one of its
  sessions waits for a reset: that is read from the waiting session, as
  `yardr session list` reads until when. `yardr doctor` without `--try`
  says which source answers for which kinds and runs nothing
  ([doctor.md](doctor.md#optional)).
- **Refused when the file is added**: a `command` that is no list of
  strings, has no `{provider}` among its arguments where the file names
  kinds, or has it in the program; a `{provider}` where the file names no
  kinds; a kind herdr starts no agent of; a provider's name that could
  be read as a flag; a kind another source of the yard already answers for
  (one source answers for a kind); `base`, `credential`, `check` or
  `notify` in a file with no `calls`.
- **It is the operator's.** Adding the file is refused from an agent's
  pane, as for every source, and here for a second reason: the yard runs
  what it names. For the same reason no pack brings one: a declaration in a
  pack with a `limits` key is refused by `yardr pack check`
  ([packs.md](packs.md#how-a-pack-maps-to-the-yard)). A crew member
  proposes a new one with a run ask, and the operator approves its card
  ([below](#a-call-by-a-session-or-a-crew-member)). aiquokka's declaration
  and command file are carried by yardr, so its run ask is `source add
  aiquokka` (above).

### A declared source

A service outside the yard, added by one YAML file: its address, the
credential it takes, and the requests the yard may make of it, each with
its parameters. `yardr source --help` has the format, and
[pushover.md](pushover.md) a whole file.

```sh
yardr source add pushover.yaml                                    # or a pack brings it: sources.<name>.declaration
yardr source key pushover                                         # type its credential: the server writes ~/.yardr/sources/pushover.env
yardr source set pushover env_file=~/.yardr/sources/pushover.env  # or name a file you keep yourself
yardr source list
yardr source show pushover
yardr source check pushover                                       # the credential, and the file's check call if it names one
yardr source rm pushover
```

`base` is the default host for calls. A service that runs in multiple
regions can also declare named bases, each held to the same checks as
`base`:

```yaml
base: https://api.example.com/v1
bases:
  us: https://api.example.com/v1
  eu: https://eu.api.example.com/v1
```

The operator chooses one by name with `yardr source set <source> base=<name>`;
`base=` restores the declaration's default. Only names in
`bases` are accepted, since this setting decides where the credential is
sent. `yardr source show` and `list` show the host in use. Calls and
commands the source names use that selected URL.

- **The file holds no secret.** It names the variable the credential is in;
  the value is read from the `env_file` at each call (mode 0600, as every
  env file). So the declaration is kept in the store, shown, and carried by
  a pack ([packs.md](packs.md#how-a-pack-maps-to-the-yard)).
- **The credential is typed, not written into a file by hand.** `yardr
  source key <name> [NAME...]` asks for the value of each variable the
  declaration names and shows nothing as it is typed; when its standard
  input is no terminal it reads `NAME=value` lines from it, which is how a
  script gives one. The value is never an argument and never in the
  command's environment. The yard's server writes it into the source's
  `env_file`, or with none set into `~/.yardr/sources/<name>.env`, which is
  then set: the line of each name given is replaced or added, every other
  line and comment is kept, and the file is replaced whole by a rename, mode
  0600. A file that is open to others, or is a symbolic link, is refused and
  not repaired, as are a name the declaration does not have, an empty value
  and one with a line break in it. It is the operator's, and is refused
  from the pane of an agent, as `yardr source set` is. What it prints is
  where the credential is set, as `yardr source show` says it; no command
  prints a value, and none is in the store, an event or the log.
  What this gives is that **no agent is shown the value without going
  looking for it**: it is typed in no shell an agent's text told a person
  to open, and it is in nothing an agent reads in the course of its work.
  It is a line, not a wall. Every agent runs as the OS user the server runs
  as, and one that runs `cat ~/.yardr/sources/<name>.env` reads the key,
  after this as before it.
- **An agent asks for a credential with a secret ask**, and the person is
  never told to paste one. `yardr bead ask <id> "<question>" --secret
  <name> [--env NAME]` stores an ask for the operator that says where its
  answer goes: the variables of this source's credential, from its
  declaration and from nowhere else. The asker names no path and invents
  no name, and asking gives it nothing: the source is one the operator
  added, and what the asker may call of it is still the operator's grant.
  The operator answers with `yardr bead answer <id> <ask> --secret`, which,
  at a terminal and once the ask's number is typed back (a secret ask is
  weighty: [the gate](chat.md#the-gate-for-a-weighty-card)), reads the value
  as `yardr source key` does and has the server write it the same way, under
  the same check, or from a device of theirs, in a window paired to this
  yard ([users.md](users.md#devices)); the answer on the bead holds the
  names (`mckean gave CRM_TOKEN`) and never the value. A pick, words and a
  decline's words are refused on such an ask and kept nowhere. It is asked
  of the operator alone and of nobody in a linked yard, and no copy of it
  crosses a link. (What a source needs of one person, their own address
  in it, is asked for with `--secret <source> --for-user <user>` and is
  that user's to give: [users.md](users.md#told-outside-the-yard).) The card says where the source's calls go, as they went
  when it was asked; when the source was replaced since by one whose calls
  go to another host, the giving is refused and the ask is asked again
  ([chat.md](chat.md#questions-for-you)).
- **The file and the store are two writes**, and nothing makes them one.
  The setting and its event are put first and committed last, after the
  rename: the store never says an `env_file` is set that the file was not
  written for. The other way round can happen. When the rename is done and
  the store then fails, the value is in the file and the store has no
  record of it: a source that had an `env_file` uses the new value from its
  next call, and one that had none has none still. The command says so,
  with the names and the file (`CRM_TOKEN is written to …, but the yard
  could not record it: … Give it again.`), and giving the same again ends
  as one giving that worked. For a secret ask the answer is in the same
  transaction: after such a failure the ask is open and has no answer,
  and giving again answers it once. Until then `yardr source show` says of a
  source with no `env_file`: `no credential; ~/.yardr/sources/crm.env
  exists and is not set: yardr source key crm, or remove it`. A change of
  an env file has an event only when it set the `env_file`: a value given
  again has none, as a file edited by hand has none.
- **A credential is sent only to the host it was given for.** The source
  remembers that host, the scheme and host of the base its calls go to,
  when `yardr source key` (or an answered secret ask) writes the credential
  and when the operator themselves sets `env_file` or `base`. Replacing the
  declaration (`yardr source add`, a pack applied again) moves nothing, so a
  declaration that now names another base, such as a placeholder a pack
  ships, does not carry the credential there. Every call of the source's own
  credential is refused (`credential_held`) before the credential is read or
  minted, naming both hosts; `yardr source check` fails the same way,
  `yardr doctor --try` makes no check call, and nothing is sent. Restoring
  the declaration it was given for ends that with no credential given
  again, and `yardr source key <name>` gives it again for where calls go
  now. A replacement that changes only the path of the base is no change of
  host. A credential given before the yard recorded this is paired once, when
  the yard first starts on this version, with the host its calls go to then.
  A call made as a user (`--as`) reads that user's own credential and is not
  held by this.

  Everything else that sends the source's credential to its host asks the
  same question first, and runs no command with the credential while it is
  held. A message told through the source (`notify`) is not sent, and the
  user is recorded as not set up to be told, in the same words. The probe of
  a chat hook's webhook is not made, and logs the words: a hook is never
  marked broken for a request that was not sent. An approved `hook add` that
  registers the webhook, and an approved `yardr source check <name> --user`,
  fail with the words and stay open, to approve again once the declaration is
  restored. The pull request and CI reads of a brief and of the daemon give
  the words in place of their answer. The thread commands (`yardr reply`,
  `yardr thread open`, `yardr thread state`, a bead's issue moving) and the
  fetch of a delivery's files are refused with them, and a move is tried again
  after its wait. A source with no `env_file` has no stored credential, and
  none of this holds it.
- **A credential that expires is minted, not kept.** The file names under
  `credential.mint` the variables that together select it, names only, and
  the operator sets the command that turns their values into a token
  (`yardr source set <name> mint_command=<program> [args...]`), run with
  `{inputs, scope}` on stdin for a `{token, expires_at}`. Its environment
  is `PATH`, `HOME`, `USER` and `LOGNAME`, the proxy and CA variables
  ([below](#commands-a-file-names)), and `YARDR_SOURCE_BASE`, the
  selected base: a helper that reads the user's keyring looks it
  up by user name, a mint behind a company's egress proxy reaches its
  service through it, and nothing else of the yard's environment reaches
  it. `USER` and `LOGNAME` are the asking process's own, each empty one taken
  from the system's user for the process instead, so a mint from the
  server (which starts with neither) runs under the same name as one from
  the command line. The token is handed to each call under
  `credential.env`, and kept in memory alone, keyed by the credential and
  the scope. Where the env sets the mint's
  names the token is minted; one of them alone is refused; otherwise the
  variable is used as it is set. A mint input is secret by default. Mark
  an input that is not secret, such as an account address, with a mapping
  item like `{ name: GOOGLE_ACCOUNT, secret: false }`; a secret ask for
  only names marked this way is shown in the clear by `yardr chat` and by
  clients that honor its `concealed` field. Mixed and secret asks
  remain hidden. This changes how the value is drawn, not where it is
  written or kept. The file may bring the command itself,
  `credential.mint_command: [<file>]`, a program beside the file that the
  yard copies under `<home>/sources/<name>/` and hashes as it does every
  command's: a card that adds or changes it shows its sha256, and a pack
  that later changes it is shown and approved again. It mints only where
  the source's own `mint_command` is unset: the operator's wins, and a
  pack applied again keeps it. The google packs bring theirs
  ([google.md](google.md#step-4-the-mint)); for a GitHub App's it is
  [`contrib/github-mint`](../contrib/github-mint/README.md), set by the
  operator.
- **What a file can say**: JSON requests and multipart file uploads over
  HTTPS, a parameter of type `secret-file` whose value names a file the
  yard reads a secret from at call time and puts as a leaf of the body
  (in no parameter, card or record), and things that are no request:
  the command that says a subscription's limits
  ([above](#say-what-a-subscription-still-carries)), what the service
  sends in ([below](#what-the-service-sends-in)), and the commands by
  which the yard speaks in the source's threads, with the source's issues
  as a bead's link ([below](#commands-a-file-names)), and what the service
  sends in about pull requests and CI, with the commands that read them
  back ([below](#pull-requests-and-ci)). A request that must be signed or
  an answer in pages is not a declared source.
  Refused when the file is added: an address that is not `https://` (plain
  `http://` to this machine only, for a stand-in), a path that leaves the
  address, a credential in a path or a query. No redirect is followed.
- **A service whose address is its secret says `url: true`.** Zapier, Make
  and n8n give a webhook an address with a path nobody can guess, and that
  address is all the credential it takes. Under `credential`, `url: true`
  makes the variable's value the address every call goes to:

  ```yaml
  credential:
    env: WEBHOOK_URL
    url: true
  calls:
    send:
      kind: update
      params: { text: { type: string, max: 2000 } }
      request: { method: POST, body: { text: "{text}" } }
  ```

  The file names no `base`, and one beside `url: true` is refused. A call
  has no path of its own: it leaves `path` out or writes `/`, and a longer
  one is refused. The value is held to what a base is when each call is
  made, since the file cannot show it: `https://`, or plain `http://` to
  this machine for a stand-in, with no user, query or fragment in it and no
  `..` or `//` in its path. The request goes to it exactly as it was given,
  trailing slash or none. The credential has no `header`, `in_path` or
  `mint`, and `{credential}` stands nowhere in a request. Nothing of the
  value reaches an error, an event or a command. An error calls the address
  "the credential's URL", and the host is cut out of the network's own
  words, such as a lookup that failed or a refused connection. A redirect
  is not followed and does not say where it pointed. No command is handed
  the variable, a `build` command included. The `source_call` event has the
  names of the parameters, as for any call. An answer that is JSON is
  returned as for any call. A 2xx whose answer is not JSON is a success with
  no answer, because the endpoint is whatever the operator pointed the URL
  at, and Make's `Accepted` is one. The `webhook` pack
  ([packs.md](packs.md)) is the whole example, with a `hook` key beside it.
- **Call parameters** are required unless `optional: true`. A parameter
  with `default: false` is also optional: when a call leaves it out, yardr
  sends the default value. The YAML value has the parameter's type and is
  checked against its pattern, length or enum values when the file is read.
  An optional parameter with no default is left out of the request when a
  call does not give it, so it stands only as a whole value in the query or
  in the body's mapping or list, where its key or item is left out with it:
  not in a path, not inside a longer text, and not as the whole body. One
  with a default is never left out, so it may stand anywhere a required one
  may, a path included. With `kind: { type: enum, values: [pages, folders],
  default: pages }`, the path `/v2/{kind}/{id}/children` is sent as
  `/v2/pages/<id>/children` when a call names no kind.
- **Who calls one**: `yardr source check`, `yardr doctor --try` (the same
  check call, of every source), a setup card (the typed account, before
  anything is stored), the daemon for a source a user
  names as their notify and for the webhook of a hook that hears a
  person's chat back ([subscriptions.md](subscriptions.md#a-quiet-hook)),
  and the server for `yardr call`, a call by a
  session or a crew member the operator granted it
  ([below](#a-call-by-a-session-or-a-crew-member)). A limits command is run
  by `yardr limits`, `yardr source check` and `yardr doctor --try`, by the
  daemon for a harness that stopped on a usage limit, at the stop and at
  the window's reset, by the dispatcher for a ready bead whose route puts
  a quota condition on a group or names a group that starts agents, and by
  the server for the web view at /limits. Nothing of a request is written to an event or a log.
- **Adding, setting and removing one is the operator's**, and refused from
  an agent's pane: a source is where a credential of the yard is sent, and
  its file may name a command the yard runs.
- **A crew member may propose three of these commands**, for the operator to
  approve on a card, with a run ask (`yardr bead ask --run "<command>"`,
  [chat.md](chat.md#questions-for-you)): `yardr source set <source>
  calls_per_hour=<n>` or `updates_per_hour=<n>`, `yardr source grant
  <source> read|<call>|--preset <preset> --group <group>|--crew <crew member>`, and
  `yardr source add <file>`, whose ask carries the
  declaration and each command file it names: for a new source the card shows
  the whole declaration, and for a name the yard has the replacement, each
  changed key whole old and new with each command file's diff
  ([below](#a-call-by-a-session-or-a-crew-member)). The approval keeps the
  source's env_file, mint_command, call limits, region base and ignored
  senders, and every grant of it stands. Where an
  embedded pack brings the source, a crew member proposes the whole setup
  as one card instead (`yardr bead ask <id> "<question>" --setup <pack>
  --crew <crew member> --recommend <n> --why "<why>" --context
  "<what it is for>"`, [chat.md](chat.md#setting-up-a-packs-sources)):
  the apply, the account and the preset grants together. Every
  other command of a source stays the operator's to type in a terminal:
  `source set env_file=`, `mint_command=`
  and `base=`, `source key` (a key is asked for with a secret ask)
  and `source rm`. Their words are a file, a path, a program or the name
  of where the credential is sent, and a command that is approved from a
  card has only names the yard holds, numbers and flags: `source add` is
  the one exception, and its file's bytes are on the card.
- **Its name is none of `peer`, `token`, `yardr`**, the sources yardr
  has of its own. With a `hook` key it is a hook source: a hook receives
  it (`yardr hook add acme --source acme --secret-file …`), and
  subscriptions hear it. Without, nothing subscribes to it.

#### Commands a file names

What a file cannot say as a request — an answer in pages, a status to set —
a file names a **command** for: one per thing the yard does in the
source's threads, under `thread`, and the source's issues as a bead's link,
as data, under `link`. `yardr reply`, `yardr thread show`, `yardr thread
open`, `yardr thread state`, `yardr link` and a hook's stage map run them
for a source that has the keys. What git uses to reach the source's
repositories is a command too, under `git`, but one the yard never runs:
git does, once a depot's git is pointed at the copy.

```yaml
thread:
  reply:  [linear/thread, reply]      # in  {thread, parent, body, key}      out {id, url}
  posted: [linear/thread, posted]     # in  {thread, key}                    out {id, url} or {}
  read:   [linear/thread, read]       # in  {thread}                         out {messages: [...]}
  file:   [telegram/thread, get]      # in  {ref, dir, max_bytes}            out {path, bytes}
  place:  team                        # what --in is called in this source
  open:   [linear/thread, open]       # in  {in, title, body, key, assignee?} out {id, title, url}
  assignee: id                        # open takes an assignee: a person's id there (or key)
  opened: [linear/thread, opened]     # in  {in, key, since?}                 out {id, title, url} or {}
  state:  [linear/thread, state]      # in  {thread, state, forward?}        out {status, moved, was?}
  states: [started, review, done]     # the words thread.state takes
link:                                 # the source's issues as a bead's link
  host: linear.app
  issue: '^/([^/]+)/issue/([A-Z][A-Z0-9]*-[0-9]+)'
  fixed: "https://linear.app/{1}/issue/{2}"   # templates over the
  thread: "{2}"                               # expression's groups, {1}, {2}
git:                                  # what git uses to reach the source's repositories
  credential: [github/git-credential] # git's protocol, not JSON; the yard copies it, git runs it
```

- **The contract** is the one the limits command runs under: a list, run
  without a shell (each word is one element), one JSON object on stdin, one
  on stdout, exit 0, at most 64 KB, 30 seconds after which the command and
  everything it started are killed as one process group. Anything else is a
  failure the yard reports with the last lines of the command's stderr,
  never its stdout.
- **`git.credential` keeps another contract, git's.** Git runs it with
  `get`, `store` or `erase` and its request on stdin; for the source's
  host it answers the token the env file sets, for any other host or
  protocol it answers nothing, and `store` and `erase` do nothing. The
  yard copies it with the declaration's other command files and never
  runs it. Refused beside no credential, or beside a credential that is
  the address every call goes to.
- **`posted` and `opened` answer `{}` for none.** Every reply and every
  opened thread is on record with a key before anything is sent, and a
  second attempt asks first, so one whose answer was lost is found, not
  posted twice. `thread.reply` takes the key as the comment's own id where
  the service takes one; `thread.open` takes it as the issue's.
  `thread.opened` is handed `since`, when the attempt was put on record in
  RFC 3339, so it can bound its search; a command that needs no bound
  ignores the field.
- **`thread.file` fetches a file a kept delivery names.** The envelope
  names each file by the source's ref (`files`, below); once the
  delivery is kept and answered, the receiver hands each ref to
  `thread.file` with `dir`, an empty directory the yard made under its
  home's `tmp/`, and `max_bytes`, the yard's `blob_max_bytes`. The
  command writes the file directly in `dir` and answers its path and
  size; one over `max_bytes`, or over a bound of the command's own, is
  refused with nothing left behind. The yard reads the file into the
  blob store and removes the directory. It runs with the source's own
  credential, from the env file `yardr source key` sets, as telling
  through the source does: the delivery is the yard's, not a crew
  member's. A source without the key, or without its credential set,
  keeps the refs alone, and the text's own words (Telegram's
  `[photo]`) say what arrived.
- **`thread.reply` takes files where the file says so.** `thread.files:
  true` says a reply carries files: `yardr reply <seq> --file <path>`
  (repeatable) puts each file as a blob and the reply's in gains
  `files`, the local paths for the command to read, each called what
  the file was called (a link to the blob in a directory of its own,
  removed after the reply), so what the service shows a person is a
  name and not the blob's hash. A source without
  the flag is refused `--file`: a command that ignores unknown keys
  would send the words and drop the files. Files are part of the
  answer, so the same words with other files are another reply, with a
  key of their own.
- **`thread.open` takes an `assignee` only where the file says so.**
  `thread.assignee` is `id` or `key`: the part of a person's identity in
  the source (`yardr user identity`) that its threads are assigned by.
  `yardr thread open --assign <user>` then hands `thread.open` that part
  as `assignee`, a string, in its object in; without `--assign` the
  object has no such key, and the thread is nobody's. A file without
  `thread.assignee` is a source whose open assigns nobody: `--assign` is
  refused there, naming the sources that take one, and its command is
  never handed the key.
- **`thread.state` moves exactly, or never backwards.** It picks the
  source's status for the word; setting moves the thread there, and only a
  thread already in the status is reported as `{status, moved: false}`. A
  stage map's move calls the same command with `forward: true`, and a
  thread at or past the status is reported as not moved. `was` is the
  status a moved thread was in; a command that does not say leaves it out,
  and the line has no "(was …)". With `thread.labels: true` the command
  also takes `label:<name>`, adding the label and never removing one, to
  any thread.
- **What the source matches in any case folds.** `link.fold: lower`
  lowercases the expression's groups before the fixed and thread templates
  expand, so one spelling names the issue however it was typed.
  `identity: {fold: lower}` keeps a crew member's keys folded, as the
  source's deliveries carry them (`yardr crew identity`). Without either,
  keys and groups keep their case.
- **A source with pull requests declares its second thread shape.**
  `link.pull` has its own `issue` pattern with `fixed` and `thread`
  templates, over the same groups in the same order: the repository's
  path segments and the number. From it the core reads a pull request's
  thread, builds its URL from repository and number, and names each
  shape's form of a thread from the other, so a bead linked with one
  form is found by an event in the other. `link.repos` is the pattern a
  repository's path matches, as a depot's origin remote carries it past
  the host: the bead an event makes goes in the depot whose repository
  the event is in.
- **A command's files.** A program with no `/` at its start is a file
  beside the declaration's file: `yardr source add` copies each into
  `~/.yardr/sources/<name>/` (the relative path kept, each mode 0700) and
  the command runs from there; `yardr source show` lists each command with
  its file and the file's sha256; `yardr source rm` removes the directory.
  An absolute program is the operator's own and is not copied. `yardr
  source check` says of each command whether its program is there and can
  be run; it runs none.
- **What a command is handed** is its environment: `PATH`, `HOME`, `USER`
  and `LOGNAME`, the proxy and CA variables, the variable `credential.env`
  names and `YARDR_SOURCE_BASE`, the selected base — so a test can
  point the command at a stand-in. `USER` and `LOGNAME` are the asking
  process's own, each empty one taken from the system's user for the
  process instead, as a mint's are. The proxy and CA variables are the
  nine of [Behind an egress
  proxy](configuration.md#behind-an-egress-proxy) (`HTTPS_PROXY`,
  `HTTP_PROXY`, `NO_PROXY`, their lower-case forms, `SSL_CERT_FILE`,
  `SSL_CERT_DIR` and `NODE_EXTRA_CA_CERTS`) as the process that runs the
  command has them: one it has not set is not set for the command, and
  one it has set and empty goes on empty. So a command that calls its
  service with curl reaches it through a company's proxy, as the yard's
  own calls do. Nothing else of the yard's environment reaches a command
  (the limits command is the exception: it runs in the environment of the
  process that asks, whole). The core
  decides which credential a command gets and hands the value in: today the
  caller's own — what the calling process's environment holds for `yardr
  reply` and `yardr thread`, what the crew member's `env_file` sets for a
  stage map's move. No command or declaration names where a credential is
  read from.
- **Refused when the file is added**: as `limits.command` is refused where
  the same reason holds (a command that is no list of strings, an empty
  program, a program with a `..` segment), and a `thread.open` without
  `thread.place`, a `thread.state` without `thread.states`, a state that is
  none of `started`, `review`, `done`, a `thread.assignee` that is neither
  `id` nor `key` or has no `thread.open`, a `link.issue` that does not
  compile, a template naming a group the expression has not, a fold that is
  not `lower`, an `identity` with no `fold`, a `link.pull` pattern with
  another group count than `link.issue`, a pull template naming a group
  it has not, a `link.repos` that does not compile. A pack for a depot
  brings neither key: `yardr pack check` refuses a declaration with a
  `thread` or `link` key there. A pack of kind `source` may bring both,
  with the commands' files
  ([packs.md](packs.md#an-integration-pack-kind-source)); `limits` is
  refused in every pack.

#### What the service sends in

What the service sends in, the file says under `hook`: how the core
verifies each delivery, the command that turns a verified body into the
envelope, and the keys its events' fields may carry. With it the source
is a hook source: a hook receives it, and subscriptions route it as they
route Linear's. `packs/linear/linear.yaml` is Linear said this
way, with the thread commands and the
link above; `internal/hooks` holds it to golden files: the envelopes for
the bodies its tests have, and what the stand-in receives for reply,
posted, read, open and state.

```yaml
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Linear-Signature, encoding: hex }
    delivery:  { header: Linear-Delivery }
    timestamp: { body: webhookTimestamp, unit: ms, window: 1m }
  bodies: unique
  normalize: [linear/normalize]
  keys: [team, project, label, sender]
```

- **Verify** stays data, checked by the core. With `hmac-sha256` it is the
  HMAC of what `signed` names with the hook's secret, made with SHA-256
  unless `algorithm` says another hash, in the
  header `signature` names, past its `prefix` (`prefix: "sha256="`, as
  GitHub sends, or none). The signature is hex or base64 as `encoding`
  says, in the standard alphabet with its padding. `signed: body` is the
  raw body alone, as Linear and GitHub say it. A service that signs its
  timestamp in says so as a template over the things the core has before
  it trusts any of them, the raw timestamp string, the raw delivery id and
  the raw body, with the service's literal text around them: `signed:
  "v0:{timestamp}:{body}"` is the basestring `v0:<timestamp>:<raw body>`,
  the timestamp read from the header `timestamp` names as it arrived,
  never a re-rendered integer and never re-serialised JSON. `{delivery}`
  is the delivery id the same way, the value of the header `delivery`
  names as it arrived, so the signature covers the id the receiver then
  dedupes on (`signed: "{delivery}.{timestamp}.{body}"`). A template
  names `{timestamp}` and `{body}` once each and `{delivery}` at most
  once. It reads `{delivery}` from a header only, so an id in the body
  refuses it when the file is added. With `secret` the header holds the
  secret itself, compared as it arrives, in constant time; `signed`,
  `algorithm`, `encoding`, `prefix`, `list` and `key` do not apply to it.

  The scheme keeps the name `hmac-sha256` for every hash: `algorithm` says
  which hash the HMAC is made with, `sha256` (what a file that leaves it
  out gets), `sha1` or `sha512`, in lower case. Intercom signs a
  notification `X-Hub-Signature: sha1=<hex>`, the HMAC-SHA1 of the body
  under the app's client secret, and the notification's own id is in its
  body:

  ```yaml
  hook:
    verify:
      scheme: hmac-sha256
      algorithm: sha1
      signed: body
      signature: { header: X-Hub-Signature, encoding: hex, prefix: "sha1=" }
      delivery: { body: id }
  ```

  The hash is the file's and never the request's. A prefix is text the
  signature is cut past and picks nothing, so a delivery that arrives as
  `sha1=…` at a file that says `sha256` is checked as SHA-256 and refused.

  A service that signs with more than one secret at a time, so it can
  replace one without a gap in which deliveries fail, sends the signatures
  side by side in one header. `list` is the separator between them
  (`list: " "`). Each entry is cut past `prefix` and decoded, and the
  delivery verifies when any one of them matches. An entry without the
  prefix is skipped, because a service that signs two ways sends both and
  only one is this scheme's. An entry that does not decode is skipped too,
  and a header with no entry left is refused. Each signature is compared
  as bytes in constant time, never as text.

  Some services hand out a secret that is not the key. `key: { prefix:
  "whsec_", encoding: base64 }` says the secret is `whsec_` followed by
  the key in base64, so the HMAC key is the bytes the rest decodes to;
  trailing padding may be present or omitted.
  Leave `prefix` out for a secret that is the encoded key alone. Without
  `key`, the key is the secret's own bytes. A secret without the prefix,
  one that does not decode, and one that decodes to nothing verify no
  delivery. Each is refused as unauthorized, with the reason in the log
  and never the secret. `whsec_` alone would make an empty key that anyone
  can sign with, so it is never used.

  Standard Webhooks (standardwebhooks.com) comes to this, with its
  timestamp in seconds in a header and a window of five minutes. The
  secret goes in the hook's secret file as the sender shows it, `whsec_…`
  and all:

  ```yaml
  hook:
    verify:
      scheme: hmac-sha256
      signed: "{delivery}.{timestamp}.{body}"
      signature: { header: webhook-signature, encoding: base64, list: " ", prefix: "v1," }
      key: { prefix: "whsec_", encoding: base64 }
      delivery: { header: webhook-id }
      timestamp: { header: webhook-timestamp, unit: s, window: 5m }
  ```

  The timestamp is checked once the delivery is verified: in the body at
  the dot path or in a header, in `ms` or `s`, within `window` of the
  yard's clock, so a replayed body whose signature covers its timestamp
  still fails on age. With `hmac-sha256` the signature must cover the
  timestamp: one in the body is covered by `signed: body`, and one in a
  header only by a template naming `{timestamp}`, so `signed: body` beside
  a header timestamp is refused when the file is added (nothing signs a
  header, and a captured delivery could be sent again with a fresh one).
  With `secret` nothing signs a timestamp, so its window does not stop a
  captured delivery from being sent again. A delivery with a bad signature
  or secret, or a timestamp outside the window, is refused, as Linear's
  is. The signing secret is handed to no command, and an unverified body
  reaches none: with `secret`, normalize is handed the request's headers
  without the one that holds it.
- **Delivery** is the service's id of the delivery: in the header
  `delivery` names, or in the body at the dot path (`delivery: { body:
  update_id }`). A retry shares the id and is answered 200 with no event,
  even where the service did not send the body byte-identical. With
  `secret` it is left out for a service that sends none, and dedupe rests
  on the body alone.
- **Bodies** is `unique` (no two deliveries from the source share a body,
  as Linear's, each signed with its timestamp), `unique-per-hook` (no two
  deliveries to one hook share a body, but one event's body goes to every
  hook that asked for it) or `repeat` (a body may come again as a new
  delivery, as a `token` sender's may). A retry of a `repeat` source is
  told by the delivery id alone, so a verified delivery that names none
  (the body lacks the field, the header is empty) is refused with 400 and
  recorded as `no_delivery`; a handshake the file's `challenge` answers is
  not a delivery and needs no id.
- **Untimed** (`untimed: true`) says the source's deliveries follow events,
  not a rhythm: a commit, not a heartbeat. Such a hook is never quiet by
  silence alone ([subscriptions.md](subscriptions.md#a-quiet-hook)), since
  an idle stretch after the last event is no break. A source that hears a
  person's chat back says nothing: it is untimed already.
- **Challenge** (`hook.challenge`) is a service's verification
  handshake, answered by the receiver and not by normalize: `challenge:
  { body: type, value: url_verification, answer: challenge }` says a
  verified body whose field at the dot path `body` holds `value` is
  answered at once with a JSON object naming that body's `answer`
  field and its value (`{"challenge":"…"}`), before normalize, dedupe
  or recording, and records no event. Verification still comes first:
  an unsigned handshake is refused as any bad signature is. A source
  with no `challenge` key behaves exactly as before.
- **Normalize** runs under the contract above: a list, run without a
  shell, one JSON object on stdin, one on stdout, exit 0, at most 64 KB,
  30 seconds. Its stdin is `{"headers": {...}, "body": ...}`: the
  delivery's headers, each with its values, and the body as JSON, or as
  text when it is none. Its stdout is the envelope, as `Envelope`
  marshals. It is handed no credential of the source's: its environment is
  `PATH`, `HOME`, `USER` and `LOGNAME`, the proxy and CA variables (a
  proxy's address may hold a password of its own), and `YARDR_SOURCE_BASE`
  alone ([what a command is handed](#commands-a-file-names)). The core sets
  `source` and `delivery`
  itself and refuses an envelope without `type`. It derives the envelope's
  `train` from its `head_branch` when the branch is a yardr train branch
  and the envelope carries no train: a script must not restate it. A
  delivery whose command fails is refused with 400 and reaches no
  subscription.
- **Keys** are what the events' `fields` may carry: a subscription's
  `--where` and a pack's `says` may name them beside `type`, `action`,
  `team`, `project`, `repo` and `label`.
- **Register** (`hook.register`) names the update call the yard makes
  to tell the service where to send deliveries, which of its parameters
  take the webhook's public URL (`url`) and, with scheme `secret`, the
  secret (`secret`), and which the hook card asks the operator for in
  the clear (`ask`): `register: { call: destination, url: url, secret:
  secret, ask: [project] }`, any other parameter fixed in the call's
  body or optional. An asked parameter is a string with a pattern the
  call sends, beside what the yard fills: the card shows a field a
  name, and the approval carries the values to the call. With scheme
  `secret` the hook card needs no typed secret: the yard makes one,
  keeps it in the hook's file, and on approval calls the register call
  at the source's selected base to register `hook_public_url` plus
  `/<hook name>` ([configuration.md](configuration.md#settings)).
  A failed call leaves the hook in place and approving again retries,
  with the values given again. Where the register call is no second run
  of itself, `existing` names the read call a retry makes first to list
  what stands, the dot path to the list in its answer (`in`), and the
  dot path in each listed webhook to the value compared with the public
  URL (`field`): a webhook already aimed at the hook is adopted, and
  the register call is not made again. The first approval always
  registers. The check is sent the URL and the asked values, never the
  secret. With a signature the service makes (`hmac-sha256`) the typed
  secret stays, and registering is a later bead. Refused: a register
  call that is no call of the file or is a read, a `url` or `secret`
  that is no string parameter of it, one the call does not send, an
  asked name that is no string parameter with a pattern of it, one the
  yard fills already, or one the call does not send, a register key
  with a scheme other than `secret`, a required parameter nothing
  fills, and an `existing` check that is no read call of the file, that
  names no list or field, or that needs a parameter beside the URL and
  the asked ones.
- **Refused when the file is added**: a scheme that is neither
  `hmac-sha256` nor `secret`, `signed`, `algorithm`, `encoding`, `prefix`,
  `list` or `key` with scheme `secret`, an `algorithm` that is none of
  `sha256`, `sha1` and `sha512` (or is no string), a signed template with
  a missing or repeated `{body}` or `{timestamp}`, a repeated
  `{delivery}`, an unknown
  placeholder, one naming `{timestamp}` with no `timestamp` key, or one
  naming `{delivery}` where the delivery id is in the body, a challenge
  with a missing `body`, `value` or `answer` or one that is no dot path
  into the body, an encoding of a signature or of a key that is neither
  `hex` nor `base64`, a `key` with no `encoding`, a `list` or a `prefix`
  that is no string, a delivery id in a header and in the body, bodies
  that repeat with no delivery, a timestamp in the body and in a header,
  or in neither, `signed: body` with the timestamp in a header, a unit
  that is not `ms` or `s`, a window that is no
  positive duration, bodies that are none of `unique`, `unique-per-hook`
  and `repeat`, an `untimed` that is neither `true` nor `false`, a command
  that is no list of strings or has no program, a key that is not lowercase
  letters, digits and `_`. Such a file needs no `base` and no `calls`. A pack for a depot brings no `hook` key; a pack
  of kind `source` may bring it with the normalize file
  ([packs.md](packs.md#an-integration-pack-kind-source)).

A source that tells people and receives can hear only its addresses
back: a `hear` key, beside `hook`, drops a delivery from anything but an
address a user's env file sets, after verification, whatever the user is
told through.

```yaml
hear:
  field: chat
  address: { env: TELEGRAM_CHAT_ID }
  from: operator
  subscribe: { type: message }
```

A source may instead hear by who speaks: `by: identity` in place of
the address, with no `address` and no `from`.

```yaml
hear:
  field: sender
  by: identity
```

A source with a `hook` key and no calls hears by identity too:
keeping a delivery by who speaks needs no call, so a hook-only source
carries the same key. Hearing by address is refused there: it names a
notify address, and such a source has none.

`fields[field]` must then hold a member id that is some user's
identity in the source (`yardr user identity <user> --source <source>
--id <id>`); the mapping is the consent to be heard, one id naming
one user, written one to one. A kept delivery carries
`fields.speaker`: the yard user's name, or `outside` for an unmapped
sender a hook allows (below). `speaker` is a key subscriptions to the
source may filter on, derived by the yard and set by no normalize
script, so it stands in no `hook.keys`; the envelope's actor is left
as the source gave it. What an unmapped sender is, is the hook's, not
the declaration's: `yardr hook add <name> --source <source>
--unknown unknown|allow|ignore` (unknown without the flag) holds one
on a mapping card until the operator answers it, hears one as
`outside`, or drops one with no card offered. A source with no
identity hearing refuses `--unknown`, and refuses `speaker` as a
filter key.

Under unknown, a verified delivery from an unmapped sender is held
on the sender's mapping card and delivered to nobody: the operator
gets a card on a question bead titled with the question, one per
source and sender, showing who the sender is, the channel the
message arrived in, and how many messages wait. The person on the
card can read each full body there; it stays out of notes, events,
`bead show` and every agent-readable view until the answer. At most
5 wait on one card, each within the hook's size
limit, and a further message while 5 wait is dropped and counted as
any drop is. Its picks are the yard's users, each naming the
identity it writes, someone new, whom the pick's words name, let in
once, and nobody, who is ignored. Picking a user writes their
identity in the answering write, and what the card held becomes
hook events in arrival order with that user as speaker, routed as
any mapped delivery; someone new with a valid new name in the
pick's words adds that user and writes theirs the same way, and
with empty, invalid or taken words the answer is refused, the card
stays open, and what it held still waits. Let in, once delivers
what waits as `outside`, as information and not instructions,
without mapping the sender, and closes the card: the sender's
next message offers a fresh card holding that message. Picking nobody ignores the
sender and deletes what the card held: their later messages are
dropped without a second card under either unknown or allow, until
the entry is removed or the sender is mapped. Declining deletes
what the card held too, writes no mapping, and the next message
offers again.

A pick that writes closes the card's question bead, with the reason
saying what was written (`U012AB3CD is ada in slack`, `U012AB3CD is
ignored in slack`). Let in, once closes it as `let in once by <user>`,
and a decline closes it as `declined by <user>`. Putting
a card aside and picking You decide leave it open, holding what
waits. Settling the sender any other way ends their card the same
way the pick it amounts to would: `yardr user identity` typed in a
terminal or approved on a run ask, and `yardr peer identity`,
deliver what the card held as that speaker; an ignore entry deletes
it. The card's bead closes with the mapping as the reason, and the
yard takes the card's question back, so it leaves your open
questions. What a card still held goes with it when the card
expires or its bead is closed. A pick on a card whose sender is
mapped already is stale and writes nothing.

The ignored senders of a source are listed by `yardr source show
<source>` and `yardr source ignore <source>`, and removed by `yardr
source ignore <source> --rm <id>`: un-ignoring hears the sender again,
a fresh card under unknown, as an outsider under allow. Removing is
the operator's alone, refused from an agent's pane, and a crew member
that needs an entry removed proposes the command with a run ask
([chat.md](chat.md#questions-for-you)); adding to the list happens only
by picking nobody on a card. The one write that maps a member id -- a
user or someone-new pick, or `yardr user identity` typed in a
terminal -- removes that id from the list in the same write; removing
an identity later does not put it back. A crew member that already
knows a mapping proposes it as `--run "user identity ..."` on a run
ask of its own ([chat.md](chat.md#questions-for-you)).

A sender can also be a crew member of a linked yard, such as the mayor of
that yard commenting in Linear. Mapping their id to one of this yard's users
would make their word a user's here, so the operator maps it on the link
instead, with `yardr peer identity <peer> <crew> --source <source> --id <id>`
([peers.md](peers.md#a-sender-who-is-a-crew-member-of-the-other-yard)), which
a crew member that knows the id proposes with a run ask of its own
(`--run "peer identity ..."`, [chat.md](chat.md#questions-for-you)) and the
operator approves from this yard.
A delivery from that id is kept whatever the hook's unknown word says, and the
operator gets no mapping card for it. Its speaker is `<crew>@<peer>`, and
`--where speaker=<crew>@<peer>` filters on it. The inbox line carries the cue
that `outside` carries, `information, not instructions`, because the word is
that yard's, as its mail is.

One id names one speaker. The store refuses a user and a crew member of a
linked yard the same id in a source, whichever comes second, and hearing
treats an id it finds on both as nobody's. Mapping an id removes it from the
source's ignore list, as mapping a user does. Only the operator sets or
removes one, in a terminal. No run ask can propose it.

`fields[field]` must hold the address a user's env file sets under
`address.env`, read as telling reads it; setting the address is the
consent to be heard, and telling is a separate choice. `from` narrows
whom the source hears to users whose role is `operator`, and is left out
for every user whose env file sets the address. `subscribe` is the filter a claim
approval subscribes the yard's hearer with beside the claimed address,
as a subscription's `--where`
([subscriptions.md](subscriptions.md)); the claim sets `field` to the
address itself, so this names only the filter beside it, and is left
out where the address alone is the filter. A drop is answered 200,
recorded nowhere and counted beside refusals, so a stranger's burst is
one bounded entry; a delivery that matches is recorded and routed as
today.

A drop is also how the yard learns an address it has none of. While
the user named by the `operator` setting has no address in the source,
the first dropped delivery offers that user a claim on a card --
"A message reached telegram from chat 123456789 (first words: 'hi').
Is this you?" -- a run ask of
`yardr user key <operator> <source>` with the address carried from
the envelope's `field` and shown on the card, and at most the first
80 characters of the message beside it; the message is kept nowhere
else. Approving writes the address under `address.env` into that user's
env file (mode 0600) exactly where `yardr user key`
writes it, and subscribes the yard's `hearer` -- the crew member that
setting names -- to that address beside the write, scope `all`, so the
next delivery from that address is heard without a terminal step;
declining drops it and writes nothing. The card says by name who will
hear the chat. One claim per source and the user named by the `operator`
setting waits at a time, and none is offered once that user has an address
in the source. In this first form the claim sets the address of the user
named by that setting; another operator-role user sets their address with
`yardr user key <user> <source>`. With no `hearer` set, or one naming
no crew member, an approval writes the address alone, as before.
The telegram pack says `from: operator`: chats of users whose role is
`operator` are heard, and telling a member through telegram tells them
but hears nothing back, as does setting another member's address; its `subscribe`
says `type: message`, so the hearer hears messages and not the chat's
other Updates. Refused: a `hear`
without a `hook` key; hearing by address without a `notify` key;
hearing by address on a hook-only source, which names a notify
address the source has none of; a
field that is no key of `hook.keys`; `by: identity` with an
`address` or a `from`, or a `by` that is no way the yard hears; an
address that is not the one `notify` reads; a `from` that is not
`operator`; a `subscribe` that is no mapping, names the field the
claim sets itself, or names a key no subscription to the source
takes. A pack of kind `source` may bring the key; a
pack for a depot brings no `hear` key, as it brings no `hook` key.

#### Pull requests and CI

What the source sends in about pull requests and CI, the file says under
`events`: the envelope types that are CI runs and that tell the pull
request's head, and the commands that read the pull request and its CI
back. The daemon judges a run against the pull request's head, reads the
head live when no event told it, and reads CI as state for a train whose
hook's table has an `event=ci` line for it, putting green and red to the
table as an event of its own. A train's brief carries CI's snapshot from
the same command. A source without pull requests says no `events` key,
and the dispatcher never asks.

```yaml
events:
  runs: [build_finished]     # the types that are CI runs, whatever became of them
  head_from: [merge_request]  # the types that tell the pull request's head
  pr: [forge/pr]  # in {repo, number, url}  out {head, base, state}
  ci: [forge/ci]  # in {repo, number, sha}  out {head, base, state, required, checks}
```

- **Runs count for the head only.** A run's result moves the bead it is
  about when the run is on the pull request's head: the `head_sha` the
  last `head_from` event carried, or, with none, the head the `pr`
  command reads live. A bead that tracks a thread which is no pull
  request, the branch of a run for none, has no head: every run on its
  thread counts. Each key stands alone: the lists without the
  commands, and either command without the other.
- **The commands run as thread commands do**: a list, run without a
  shell, one JSON object on stdin, one on stdout, exit 0, at most 64 KB,
  30 seconds. The `pr` command answers the pull request's head commit,
  its base branch and its state (`open`, or what closed it); the `ci`
  command answers the same of the pull request with the commit's CI: the
  names its base requires, and the checks on the commit, each with its
  status and conclusion. The core refuses an answer with no head or base.
- **Checks speak one tongue.** A check is `completed` or not; a
  completed one concluded `success`, `neutral`, `skipped`, `failure`,
  `timed_out` or `startup_failure`. The script translates its service
  into them, as a state script translates statuses into `started`,
  `review` and `done`; the verdict stays the core's. Green is every
  required check concluded well, red a required one failed or timed out,
  pending anything else; a base that requires no check lands nothing.
- **Whose credential reads is the caller's.** The daemon's reads run
  with the hook's `as` crew member's credential, read as for a move;
  the brief's snapshot runs with the source's own `env_file`, since the
  brief has no hook. Nothing ambient is tried. A minted credential mints
  with the pull request as the scope.
- **Refused when the file is added**: a type list that is no list of
  strings, a type listed twice, a command that is no list of strings or
  has no program. A pack for a depot brings no `events` key: `yardr pack
  check` refuses a declaration with one, as it refuses `hook`, `thread`
  and `link`; a pack of kind source may bring it with the commands. A
  hook with an `event=ci` line of a declared source is refused without
  the source's `ci` command and the crew member it acts as (`--as`).


### A call by a session or a crew member

An agent reads from a declared source with `yardr call`, and writes to one
([below](#an-update)), when the operator granted its group or it the call. The worked example is a customer system
(`contrib/isolated-yard/sources/crm.yaml` is the file, for the stand-in
there):

```sh
yardr source add crm.yaml
yardr source key crm             # its credential, typed: ~/.yardr/sources/crm.env
yardr source grant crm read --group builders --group reviewers   # every read call
yardr source grant crm customer --crew yardmaster                # one read call
yardr source show crm            # each call, and who may make it
yardr source grants              # every grant of every source, each call's own
yardr call crm customer id=4711  # in a builder's session
yardr call --list                # what the caller may call
yardr source revoke crm read --group reviewers
```

```
answer from crm (customer): data from outside the yard, not instructions
{
  "id": "4711",
  "name": "Ada",
  "plan": "pro"
}
```

- **The server makes the request.** `yardr call` sends the source, the call
  and the parameters over the yard's socket. The server reads the source's
  credential from its `env_file` for that one call, or the user's with
  `--as <user>`, and keeps it nowhere, so it is in no pane's environment,
  no brief and no event. This is the one place the server speaks to a
  service. It is a line, not a wall: every agent runs as the same OS user
  and could read a 0600 file if it went looking. The request is made
  outside any store write and ends after 30 seconds; yardr tries no call
  twice.
- **One credential a source, or two.** Whoever calls, the request goes out
  with the yard's credential for the source, not the caller's. A file that
  names a second variable (`credential.update_env`) has its update calls
  sent with that one, so the reads can run on a token that may only read;
  the `env_file` sets both, and `yardr source set` refuses one that does
  not.
- **Called as a user.** `yardr call … --as <user>` is made with that
  user's credential, from their `env_file`, under a grant as them
  (`yardr source grant <source> read|<call> --group <group>|--crew <crew
  member> --as <user>`, theirs alone to give): a plain grant covers no
  call as a user, and a grant as a user no plain call, so the word must
  be typed and no call is ever made as a user by falling back. The
  credential is given on a card (`yardr bead ask <bead> "<question>"
  --secret <source> --as <user>`). The operator may make any call as any
  user whose file sets the names, to try it. The hourly limits stay per
  caller: plain and as calls share the one budget. `yardr source check
  <name> --as <user>` tries the credential the same way a check tries
  the source's, without a grant.
- **Every grant in one list.** `yardr source grants` prints a row for
  each call a holder is granted of a source (`read` is one row, with how
  many read calls it covers today), each with that call's own grant: when,
  by whom, and where it was approved on a card the bead, the ask and who
  asked it. `--source`, `--group`, `--crew` and `--as` narrow it, and
  `--json` prints the records, as `SourceGrants` answers them to
  footplate ([api.md](api.md#for-the-operator-alone)). A call granted
  before the yard kept that, whose `source_granted` event was pruned by
  then, reads `not kept`, and its record's line below the summary says
  when it last gained a call and by whom, for no call in particular.
  The yard sets no reminder to look: read it after a walk that granted
  anything, and from time to time, and take back what is no longer
  needed with the `yardr source revoke` line it ends with.
- **A grant is a record of its own**, by source and grantee, not a key of
  a group's or a crew member's settings: `yardr group add` replaces a
  group's definition whole and a pack's import writes groups, so a grant
  kept there would be dropped by the one and could be carried in by the
  other. `read` is every read call of the source, one a later file adds
  included; `yardr source add` says who gets a call that is new or changed
  through it. A call's name grants that one call, and is the only way an
  update call is granted. `--preset <name>` grants one preset's calls of
  the source instead, from the embedded pack that declares it (`yardr
  pack show <pack>` prints a pack's presets); each call is granted as if
  by name, and taken back by name. Granting is the
  operator's and is refused from an agent's pane whatever `--actor` says;
  with `--as <user>` it is that user's alone to give, the operator refused;
  anyone may revoke. That refusal is a line against mistakes, not a wall:
  outside a pane the actor is a claimed name
  ([users.md](users.md#who-is-acting)), so it does not stop a terminal that
  claims the user, nor an agent that starts a process outside its pane's
  process tree, which is then taken for one. A crew member that needs a grant proposes it with a
  run ask (`yardr bead ask <id> "<question>" --context … --run "source
  grant crm note --crew yardmaster"`): the card names each call with its
  method and path, says of `read` which calls that is today and that it
  covers read calls the source is later given, and says of an update call
  that it changes something there. A preset grant names the preset and
  the pack on the card beside each grantee. The approval is held to the
  declaration the card was written from, to the preset's bytes with the
  build where one is named, and to each group and crew member as it was:
  a source that was replaced or given another `env_file` or `mint_command`
  since, a preset whose calls differ, and a group or crew member whose
  config changed since, make the ask stale, and it is asked again. A source's two limits are proposed the
  same way, a lower number too, since every setting of a source is the
  operator's; the card then says that only the operator takes it back. `yardr source show`, `yardr crew show` (`may call crm:
  customer`) and `yardr group list` (the `CALLS` column) print them.
  Removing a source removes its grants.
- **A pack asks, the operator answers.** A pack may ask for calls beside
  the declaration it brings: `groups.builders.calls: { crm: [read] }`,
  `crew.yardmaster.calls: { crm: [customer, note] }`. It is a request, as
  `open_threads` is: the import puts each to the operator, once per group
  or crew member and source, with no as the default (`--calls
  builders=crm:read` answers without a terminal), and only once the
  source's `env_file` is set. A yes is this grant; a no is recorded and not
  asked again. A pack's declaration that adds a read call says in its plan
  who gets the call through a grant of `read` they hold
  ([packs.md](packs.md#how-a-pack-maps-to-the-yard)).
- **Who the caller is** the server tells from its own records, in this
  order: the session whose pane the command runs in, which gives its group
  and its bead; else the crew member whose pane it is; else the session
  `$YARDR_SESSION` names; else the actor, a crew member by its name or the
  operator. A pane that names another session than its own is refused. A
  session that has ended calls nothing. The operator may make any call, to
  try one; any other person makes none. A script stage is a
  session of its exec group and calls under that group's grants.
- **A session whose bead came from a linked yard calls nothing**
  (`from_peer`), whatever its group is granted: its brief is that yard's
  text, and what it reads could go back in its notes.
- **How an agent learns of its calls**: its brief has a `Sources` section
  with each call as its command, `yardr prime` lists them, and `yardr call
  --list` prints the same. An update is marked `(update)` in each.
- **The answer is data.** It is printed under the line above, as mail from
  another yard is marked ([peers.md](peers.md)). `--json` prints the answer
  alone. An answer is JSON, after the call's `select`, 64 KB at most unless
  the call's `answer.max` raises it; a larger one is refused, not cut. No
  redirect is followed: the 3xx fails the call. A source whose credential
  is the address (`url: true`) keeps a 2xx answer that is not JSON as a
  success with no answer.
- **Limits.** One caller makes 60 calls of a source in an hour (`yardr
  source set <name> calls_per_hour=<n>` changes it), 10 of them updates at
  most (`updates_per_hour=<n>`); the call after is refused (`limit`) until
  the hour that began with the first has passed. An update is counted as a
  call and as an update.
- **The answer as a note.** `yardr call … --note` also writes the answer on
  the bead of the session that calls, under the same line, for whoever
  works the bead next. Nothing is noted unasked. An answer over 16 KB as a
  note is printed and not noted, and the command fails: it is not cut. It
  is refused before anything is sent for a caller that works no bead (a
  crew member, a person: `bad_param`), and on a bead that came from a
  linked yard, whose notes go back to that yard with its return
  ([peers.md](peers.md#the-return)).
- **The record.** Every call, made or refused, is a `source_call` event,
  on the caller's bead for a session: `source`, `call`, `kind`, `by`,
  `session` and `group` or `crew`, `as` where a call named one, `status`,
  `ms`, `bytes`, and `params`, the names of the parameters given. `status`
  is the service's HTTP status,
  or why nothing was sent or returned: `not_granted`, `from_peer`, `limit`,
  `hosted_google` (an agent call waits for in-context confirmation),
  `credential_held` (the source's credential was given for another host than
  its calls go to now),
  `bad_param`, `no_credential`, `refused`, `unreachable`, `timeout`,
  `redirect`, `too_large`, `not_json`. The value of a parameter is kept
  (under `values`) only when the file marks it `log: true`, for what an
  audit asks by. `attempt` is there, from 2, for an update sent again with
  the key of a try before it. Never kept: the credential, the request, the
  answer, the address with its values, the key.
- **What is wrong with one** is in `yardr doctor`'s `sources:` line and its
  notes: a credential file that is not set, missing, open to others or
  without the name; a credential held back from the host its calls go to
  now; a source whose last answered call the service refused
  with a 401 or a 403, read from these events; a source nobody is granted a
  call of. Doctor sends nothing. `yardr doctor --try` makes each source's
  check call, as `yardr source check <name>` does for one
  ([doctor.md](doctor.md#optional)).

#### An update

A call of `kind: update` changes something in the service: a note on a
customer. It is made with the same command, under everything above, and
with three things more.

```yaml
credential:
  env: CRM_TOKEN
  update_env: CRM_UPDATE_TOKEN          # optional: what an update is sent with
  header: "Authorization: Bearer {credential}"
idempotency: Idempotency-Key            # optional: the header the service takes a key in
calls:
  note:
    kind: update
    about: Add a note to a customer
    params:
      id:   { type: string, pattern: "^[0-9]{1,12}$", log: true }
      text: { type: string, max: 2000 }
    request: { method: POST, path: "/customers/{id}/notes", body: { text: "{text}" } }
```

```sh
yardr source grant crm note --group support      # by its name: read does not cover it
yardr source set crm updates_per_hour=3          # 10 unless set
yardr call crm note id=4711 text="asked for a copy of the invoice"   # in a session of support
```

- **Granted by name.** There is no word for every update, and `read` covers
  none: each update call is granted to each group or crew member by its
  name. `yardr source show`, `yardr group list` and `yardr crew show` mark
  it (`crm: note (update)`). A file that changes a call somebody holds by
  its name says so when it is added, with the command that takes the grant
  back: the grant was given for the call as it was.
- **Nothing more is asked.** No person confirms an update: the grant is the
  permission. What bounds an agent that went wrong is the hourly limit and
  what the service's own credential may do.
- **A call run again writes once**, where the service can tell. When the
  file names the header (`idempotency`), each update is sent with a key, a
  UUID the yard chooses and keeps before the request. The same caller
  making the same call with the same values again as the same user, after
  a try that did not end in a success (a timeout, a 500, a connection that
  broke), is sent with the same key, so a service that took the first
  writes nothing more: the key names who it was called as too, so two
  users' same call are two keys. After a success the same call is a new
  one. yardr itself tries nothing twice: the caller runs the command
  again. A key nobody tried again is dropped after a day, and the store
  keeps a hash of the values, not the values. Without the key in the file
  a second try is a second write.
- **In the record** the event has `kind: update`. A note's text is free
  text and is not marked `log: true`.

### What is not there

- Telling a user through Linear or GitHub (a DM, a mention): neither pack
  has a `notify` key (GitHub gives an App no direct message to send).
- A way to be told that is no HTTPS request with a JSON body: a desktop
  notification, mail by SMTP, a program of the operator's. The place for
  one is behind the same four values; none is built.
- Per user: which parts of the inbox are told, how much of the text, quiet
  hours, the grace and the hourly bound (all fixed), a link a phone can
  open, several sources at once. For a crew member or a group: nothing.
- Events in from a declared source except by a webhook it signs (the key
  `events` is kept for whatever else one sends).
- For a call by an agent: opening a source to the beads of one linked yard, a person who
  confirms an update before it is sent, a grant for a user who is not the
  operator, a setting for the 30 seconds a call may take, calls served as
  tools (MCP). An update of a service that takes no idempotency key is
  written twice when it is run again after a timeout.
- Limits, beyond showing them, saying them in the event of a harness that
  stopped on one, prompting it again when a window that resets within six
  hours opens, and keeping a group from starting a session while a window of
  its provider is used up: a second way to an answer (a
  declared HTTPS call, an adapter in Go), limits by model (a window may say
  its model, which only keeps it from holding a group that names no
  condition and from being the window a stopped agent waits for while
  another is used up; no group or agent is matched to the model of a
  window), the limits of an account that is not the operator's.

## How one becomes part of a yard

The steps as they are today, with Linear, for a crew member called `yardmaster`.
"Anyone" is anyone who runs yardr on the yard's home; "the operator" is the
yard's `operator` setting, else the user running the command.

Every command below was run in an isolated yard, with a stand-in for
Linear's API where a step calls it, and the output shown is what it
printed; the home, the port (a free one there) and the paths are written
here as a yard usually has them. What needs a real Linear workspace is
marked *(in Linear)*.

### 1. An address the service can reach

*Anyone.* The yard's server takes webhooks on one TCP address, the
`hook_addr` setting, and reads it when it starts:

```sh
yardr settings set hook_addr 127.0.0.1:65207
yardr server stop        # the next command starts it again, on the address
yardr doctor             # … note  hooks: the server takes webhooks on 127.0.0.1:65207 (the hook_addr setting)
```

The listener serves `POST /hooks/<name>`, the yard's peers under `/peers/`
and, in a yard that has one, the devices of its users under `/user/`
([users.md](users.md#devices)), and nothing else. The service must be able to reach it: the yard needs an HTTPS URL
that arrives at `hook_addr`'s `/hooks/<name>`. How that URL comes to be is
outside yardr; let what stands in front pass `/hooks/` and nothing more.
yardr does not know its public address and never calls the service to
register a webhook ([configuration.md](configuration.md#settings)).

The listener speaks plain HTTP, and yardr does not do TLS itself. An address
that is reachable from beyond a network you trust needs a TLS terminator in
front of it (a reverse proxy or a tunnel of your own), and the `https://` URL
given to the service is the terminator's. Without one every delivery crosses
the network in the clear: anyone on the path reads it, and from a `token` or
`secret` hook's delivery reads the secret too, which is all it takes to send
deliveries of their own. A signed delivery keeps its secret off the wire, but
is read all the same.

### 2. The pack

*The operator.* Linear comes as the `linear` pack: applying it declares
the `linear` source — how its webhooks are verified and read, and the
commands the yard replies, opens issues and sets their status with:

```sh
yardr pack apply linear --yes
# pack linear (embedded in yardr …), kind source, into the yard
# …
# applied pack linear to the yard: 1 created, 0 changed, 0 unchanged
# source linear is declared: 1 call under https://api.linear.app (yardr source show linear)
# …
```

The apply ends by printing the pack's setup notes, which the steps below
follow and show the output of. The pack carries no secret and sets no
identity: the secret file, the token and the identity below are the
yard's to add. `yardr pack show linear` prints the notes again
([packs.md](packs.md)).

### 3. The hook and its secret

*Anyone.* The secret the service signs with goes in a file, mode 0600, and
the hook names the file. A new file takes its mode from the shell's
umask, so narrow it first. The secret is never a command's word: that
line would stay in the shell's history. Each command below is one thing
to type, in a block of its own; run the second, type or paste the secret
blind and press Enter:

```sh
umask 077
```

```sh
IFS= read -rs v && printf '%s' "$v" > ~/.yardr/linear.secret; unset v
```

```sh
chmod 600 ~/.yardr/linear.secret
```

```sh
yardr hook add linear --source linear --secret-file ~/.yardr/linear.secret --unknown allow
# hook linear saved: POST /hooks/linear (linear)
```

A file open to others is refused (`… is open to others (mode 0644); chmod
600 it`). The file is read on every delivery, so rotating the secret is
rewriting the file ([subscriptions.md](subscriptions.md#the-secret-file)).
One hook serves every crew member. `--unknown allow` hears every
workspace member's comment: a delivery from a sender no yard user maps is
kept with the speaker `outside`, and one from an id a user's identity
holds arrives named as them
([above](#identify)).

A crew member sets the hook up without handling the secret, by two asks
on one bead, the secret first. It asks for the signing secret
(`yardr bead ask <id> "…" --secret-hook linear --source linear --context
"…"`): the operator pastes the secret where the service shows it, typed
unseen (`yardr bead answer <id> <ask> --secret`, or `ctrl+o` in chat), and
the yard's server writes it to `<home>/hooks/linear.secret` (mode 0600),
which the hook then names; the value is in no note, event, mail, brief or
pane. It then asks for the hook itself
(`yardr bead ask <id> "…" --run "hook add linear --source linear --unknown allow" --context
"…"`): the card says `POST /hooks/linear`, the source, and that it takes
the source's signing secret from that file, and approving it
(`yardr bead answer <id> <ask> --approve`) makes the hook, which takes
deliveries only with the secret standing in its file. The asks are refused
for a source the yard does not declare, one whose declaration has no hook
key, and a hook the yard has already; `--map` and `--as` stay in a
terminal, as a replacement does. The steps by hand above stay the way to
make or replace one in a terminal.

Where the declaration names a register call with scheme `secret`
(Telegram), there is no secret ask: the one `--run "hook add <name>
--source <source>"` card says the yard will make the secret and call
`<method path>` at the source's base to register the `hook_public_url`
URL for the hook, and approving makes the secret and the hook and
makes that call. No card is written while `hook_public_url` is unset.
Giving the yard that address is a card of its own: a crew member proposes
`settings set hook_public_url <url>` as a run ask, and its card shows the
whole URL each hook is registered at.

*(in Linear)* Settings → API → Webhooks: the URL
`https://<public host>/hooks/linear`, the events Issues and Comments. Its
signing secret is what goes in the file.

Check: `yardr hook list` shows the hook with STATE `ok`, and `-` under LAST
ACCEPTED until a delivery came.

### 4. The crew member's credential

*Anyone, for the file; the token is made in Linear.* What the crew member
speaks with goes in an env file of its own, never in the store. A new
file takes its mode from the shell's umask, so narrow it first. The
token is never a command's word: that line would stay in the shell's
history. Each command below is one thing to type, in a block of its own;
run the second, type or paste the token blind and press Enter:

```sh
umask 077
```

```sh
IFS= read -rs v && printf 'LINEAR_API_KEY=%s' "$v" > ~/.yardr/crew/yardmaster.env; unset v
```

```sh
chmod 600 ~/.yardr/crew/yardmaster.env
```

```sh
yardr crew set yardmaster env_file=~/.yardr/crew/yardmaster.env
yardr crew show yardmaster
#   env_file …/crew/yardmaster.env (LINEAR_API_KEY)
#   linear credential: API key (LINEAR_API_KEY)
```

`env_command` names a command that prints the environment in place of a
file ([crew.md](crew.md#secrets-from-a-provider)). The file is
read when the crew member starts: `yardr crew restart yardmaster` after a
change.

*(in Linear)* The token is that of a Linear OAuth application made for the
crew member, so the crew member acts as the application's own user and not as a
person ([subscriptions.md](subscriptions.md#setting-up-linear)).

Nothing checks the token against Linear at this step: `crew show`
reads the names in the file, not whether the service takes the value.

### 5. The crew member's identity

*Anyone.* The id and the profile key of the user the token acts as:

```sh
yardr crew identity yardmaster --source linear --id <user id> --key <profile key>
# SOURCE  ID                                    KEY
# linear  11111111-2222-4333-8444-555555555555  yardr
```

Both are looked up in Linear by hand; yardr has no command that asks the
service who a token is.

### 6. The grants

*The operator only.* Answering needs none. Opening issues and setting
their status do, and the stage map of step 8 needs `set_state`:

```sh
yardr crew set yardmaster open_threads=linear set_state=linear
#   open_threads: (not set) -> linear
#   set_state: (not set) -> linear
```

From anyone else: `open_threads: only the operator (jo) may let yardmaster open
threads in linear; this is mckean`.

### 7. Subscriptions

*Anyone, for a crew member.* What of the source reaches the crew member's inbox:

```sh
yardr subscribe yardmaster linear --scope involved      # mentions, what is assigned or delegated to it, its threads
yardr subscribe yardmaster linear --scope all --where type=issue --where action=create --where team=YRD
```

The second is "a new issue of team YRD reaches the yardmaster", whoever it
mentions. The where keys of a hook source are `type`, `action`, `team`,
`project`, `repo`, `label`; GitHub has more
([subscriptions.md](subscriptions.md#github)). Linear's deliveries carry
`team` (key and name), `project`, `label` and `sender`, and a kept one
carries `speaker`: the yard user an identity maps the sender to, or
`outside` for one none maps. `--where speaker=<user>` hears one person's
deliveries. A key that is none of the source's is refused.

### 8. The stage map

*The operator, or the crew member's own session.* Which stage means which
status is set on the hook, with the crew member the yard acts as. `hook add`
replaces the definition, so the source, the secret file and `--unknown
allow` are given again:

```sh
yardr hook add linear --source linear --secret-file ~/.yardr/linear.secret --unknown allow --as yardmaster \
    --map task:new=started --map task:review=review --map task:merged=done
# hook linear saved: POST /hooks/linear (linear)
# moves tracked linear issues as yardmaster: task:merged=done task:new=started task:review=review
yardr hook show linear
# linear  linear  POST /hooks/linear
#   secret file …/linear.secret
#   acts as yardmaster
#   map task:merged=done
#   map task:new=started
#   map task:review=review
```

The crew member must have an identity in the source (step 5), and nothing is
moved without `set_state` (step 6). The daemon makes the calls, so it must
run.

### 9. A test delivery

Before the service sends anything, a delivery signed by hand shows that the
listener, the hook, the secret file and the subscriptions hold. This is how
the pack's tests sign theirs: the hex HMAC-SHA256 of the body with the
secret, and a `webhookTimestamp` of now in the body.

```sh
body='{"action":"create","type":"Issue","actor":{"id":"aacdca22-6266-4c0a-ab3c-8fa70a26765c","name":"Jane Doe"},"url":"https://linear.app/acme/issue/YRD-1/web-view-shows-links","data":{"id":"539068e2-ae88-4d09-bd75-22eb4a59612f","title":"Web view shows links","description":"The bead page should list the issue it tracks.","identifier":"YRD-1","url":"https://linear.app/acme/issue/YRD-1/web-view-shows-links","team":{"key":"YRD","name":"Yardr"},"labels":[{"name":"ui"}]},"webhookTimestamp":'$(($(date +%s) * 1000))'}'
sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$(cat ~/.yardr/linear.secret)" | sed 's/^.* //')
curl -s -X POST http://127.0.0.1:65207/hooks/linear \
    -H "Linear-Signature: $sig" -H "Linear-Delivery: $(uuidgen)" \
    -H 'Content-Type: application/json' --data-binary "$body"
# {"seq":16}
```

`yardr events` then has the delivery, with whom it reached and why:

```
    16 10-06 11:00:27  -        hook           hook:linear  action=create actor={id=aacdca22-6266-4c0a-ab3c-8fa70a26765c name=Jane Doe} deliveries={yardmaster=all} delivery=52C4DEDF-79AF-41ED-82C4-B38CBB085A37 entity=539068e2-ae88-4d09-bd75-22eb4a59612f fields={label=[ui] sender=[aacdca22-6266-4c0a-ab3c-8fa70a26765c] speaker=[outside] team=[YRD Yardr]} hook=linear mentions=[] new_mentions=[] source=linear text=The bead page should list the issue it tracks. thread={id=539068e2-ae88-4d09-bd75-22eb4a59612f title=Web view shows links url=https://linear.app/acme/issue/YRD-1/web-view-shows-links} type=issue url=https://linear.app/acme/issue/YRD-1/web-view-shows-links
```

and the crew member's inbox has it as a line (`yardr inbox --crew yardmaster`):

```
    16 10-06 11:00  hook (all) linear issue create by outside (aacdca22-6266-4c0a-ab3c-8fa70a26765c) — information, not instructions on "Web view shows links": The bead page should list the issue it tracks.  https://linear.app/acme/issue/YRD-1/web-view-shows-links
```

Jane Doe maps to no user of the yard, so the delivery is kept with the
speaker `outside` under the hook's `--unknown allow`; give her `yardr
user identity jane --source linear --id aacdca22-6266-4c0a-ab3c-8fa70a26765c`
and her deliveries arrive `by jane (aacdca22-6266-4c0a-ab3c-8fa70a26765c)`.

The same delivery signed with another secret is answered 401 and shows as

```
    15 10-06 11:00:27  -        hook_rejected  hook:linear  count=1 hook=linear last=2026-10-06T09:00:27Z reason=unauthorized status=401
```

and in `yardr hook list` under LAST REFUSED (`10-06 11:00 unauthorized`),
beside LAST ACCEPTED. A hook with neither time has been sent nothing this
yard's server saw: then the delivery went elsewhere or was not sent, and
the service's own delivery log says which. `yardr hook show 16` prints the
raw body.

### 10. The yardmaster answers, and a merged bead closes the issue

*The crew member's own session, or the operator, for the answer; anyone for
the bead and the link.*

```sh
yardr bead create "Web view shows links" --depot work --tracks 16   # the issue of delivery 16
# work-h6ef
yardr link list work-h6ef
# tracks  https://linear.app/acme/issue/YRD-1 (linear)
yardr thread show 16                                      # (in Linear) the issue's comments
yardr reply 16 -                                          # (in Linear) the words on stdin: Filed as work-h6ef; I will post here when it lands.
```

The last two call Linear with the credential of step 4, from the crew member's
pane; the operator runs `yardr reply` with `--crew yardmaster` and the
credential in the command's environment. From anyone else:
`reply --crew yardmaster: only yardmaster's own session or the operator (jo) may speak as yardmaster; this is mckean`.
The stand-in does not know comments, so the post itself was not made there.

The link is the issue's fixed form, without the title the delivery's URL
had. A bead created into a mapped stage moves the issue at once, and each
later stage does as the map says:

```sh
yardr bead advance work-h6ef --to merged
yardr events --bead work-h6ef
#   21 … work-h6ef thread_state  hook:linear  cause=18 hook=linear crew=yardmaster source=linear stage=new state=started status=In Progress … was=Todo
#   24 … work-h6ef thread_state  hook:linear  cause=23 hook=linear crew=yardmaster source=linear stage=merged state=done status=Done … was=In Progress
```

In a yard with a flow the bead reaches `merged` through review and the
assembly ([line.md](line.md)); the map hears the stage however the
bead got there. While a move waits or is retried, `yardr hook show linear`
lists it (`to do: work-h6ef (task in new) -> https://linear.app/acme/issue/YRD-1 started`).

### GitHub, where it differs

GitHub comes as the `github` pack: applying it declares the `github`
source — how its webhooks are verified and read, the credential it takes,
and the commands the yard replies, opens issues, sets their status and
reads pull requests and CI with. The pack's setup notes
(`yardr pack show github`) walk the steps; the session is:

```sh
yardr pack apply github
# applied pack github to the yard: 1 created, 0 changed, 0 unchanged
# source github is declared: 1 call under https://api.github.com (yardr source show github)
yardr source set github env_file=~/.yardr/sources/github.env
yardr source check github
# github: credential: GITHUB_TOKEN is set in ~/.yardr/sources/github.env
# github: its file names no check call (check: <a read call without parameters>), so nothing was sent
# …
```

To let landings push a linked depot to its origin, a crew member or user can
file `yardr bead ask <id> "<question>" --depot-push <depot>`. The operator
approves the outside-weight card; approval adds `YARDR_PUSH=origin` to that
depot's `<depot>-assembly` command and sets the matched source's declared
path-only credential helper in that depot's local Git config; the source's
token must stand to turn push on. Approval does not run `git push` or put the
token in the card. Turning push on is refused while any other credential
helper answers for the origin's host, one set for every host or box-wide
included (some git builds bring their own), since git hands the token to
each helper it runs. File the reverse with
`yardr bead ask <id> "<question>" --depot-push-off <depot>`.

The same steps as Linear, with these differences:

- **The hook.** `yardr hook add gh --source github --secret-file
  ~/.yardr/github.secret`. A repository webhook is set per repository, with
  content type `application/json` and the secret from the file. The
  delivery is signed in `X-Hub-Signature-256: sha256=<hex HMAC-SHA256 of
  the body>` and names its event in `X-GitHub-Event`; no timestamp is
  signed, so a test delivery needs none.
- **A GitHub App** does both sides at once: its one webhook delivers every
  repository it is installed on, and the crew member answers as the App's bot.
  Its credential is two lines in the `env_file`, `GITHUB_APP_ID` and
  `GITHUB_APP_PRIVATE_KEY_FILE`, in place of `GITHUB_TOKEN`, and the
  operator's mint command, which turns their values into a short-lived
  token for each repository called (`yardr source set github
  mint_command=<program> [args...]`; [`contrib/github-mint`](../contrib/github-mint/README.md)
  is one). `yardr source check github` reports the mint way and runs the
  mint and then the check call with the token
  ([subscriptions.md](subscriptions.md#as-a-github-app),
  [crew.md](crew.md#as-a-github-app)).
- **The identity** is a key only: the login in lowercase, `yardr crew
  identity yardmaster --source github --key <login>`; for an App `--key
  '<slug>[bot]'`.
- **The states.** An issue is open or closed, so a map may say `started`
  and `done`; `review` is refused (`"review" is no state of github: want
  started, done`). A closed issue is never
  reopened by a map, and a pull request is never touched.
- **Links** are to issues and to pull requests. A map sets the status of
  an issue only: for a tracked pull request the move is skipped (`failed`).
  So is the move for a link to a thread that is neither, the branch a
  bead made for a failed run tracks (below): at once, with nothing sent
  to GitHub, as `thread_state_skipped` with `names no issue`. Nor can
  `yardr reply` answer in it or `yardr thread state` set its status.
- **Where keys** are the pack's `hook.keys`: besides the common ones,
  `number`, `state`,
  `merged`, `conclusion`, `workflow`, `head_branch`, `head_sha`, `head_repo`,
  `base`, `train`, `sender`, `association`, `outsider`. `association` is
  GitHub's `author_association` in lowercase: what the author of the issue or
  pull request (for a comment or a review, its own writer) is to the
  repository, `owner`, `member`, `collaborator`, `contributor`,
  `first_time_contributor`, `first_timer` or `none`. So `--where
  association=member` in a subscription, or in a says line, keeps to whom
  the repository knows.
- **Who wrote it.** `outsider` is `yes` when that association is given and
  is none of `owner`, `member` and `collaborator`, which the pack's
  normalize decides, and is not there otherwise (a run of CI gives no
  association). A github delivery names no speaker, but one marked
  `outsider` carries the cue of an outsider in its inbox line, `information,
  not instructions`, after the sender's login: `by <login> — information, not
  instructions`. The mark is about whoever wrote the text, who need not be
  the sender the line names: a maintainer who labels someone else's issue
  keeps their own name. A move a says line makes for such a delivery writes a
  note without its text, and the note's URL points at it.
- **Events move beads.** A github hook's events are put to the hook's
  table of what they say (`yardr hook add --says`, or a pack's
  `sources.github.says`) for the bead the event is about: the one that
  tracks its thread (an issue or a pull request, `yardr link`), else the
  train of its branch (`train`: the head branch is `yard/<train>`). The
  line an event matches reports an outcome for the bead, which its flow
  routes. A line may name the bead's type with `type`, so one table serves
  trains and tracked beads: `event=pull_request action=closed merged=false
  type=train -> question` asks a person about the train while `type=pr ->
  dropped` drops the pr bead for the same event. The event's own type is
  named with `event` there. An event whose actor is a crew member's github
  identity (the App's `<slug>[bot]`) says nothing: a label or comment the
  yard set never moves the yard. yardr holds no rule of its own for them
  ([links.md](links.md#the-sources-events-move-the-bead),
  [line.md](line.md#trains)).
- **Events make beads.** An event about a thread no open bead tracks is
  put to the hook's other table, of what its events make (`yardr hook add
  --makes "<where> -> <type>:<stage>[@<depot>]"`, or a pack's
  `sources.github.makes`), so that an issue or a pull request from outside
  enters the yard: `--makes "event=issues action=opened association=none
  association=first_time_contributor -> issue:triage"`. The first line the
  event matches makes a bead of that type in that stage, with the thread's
  title, the event's text and url as its body and a `tracks` link to the
  thread, as `hook:<name>`; the bead has its `created` event and a
  `hook_rule` event with the line under `makes`. The where keys are the
  ones above, `association` among them, without `stage` and without `type`,
  which the line's bead already says; the event's own type is named with
  `event` there, as in a `says` line. The depot is the
  line's, else the one depot whose `origin` remote is the event's `repo`
  (read at each event); with none, or several, nothing is made and a
  `hook_rule` event on no bead says why under `ignored`. One thread makes one bead: a delivery that
  comes twice, or a draft marked ready after it was opened, finds the bead
  the first event made, and says to it what the other table has for it. A
  closed bead that tracks the thread does not stand in the way: the next
  event after the close makes a new bead, which takes the link, and the
  closed one keeps a `relates` link. A line is refused when the type's
  flow in the depot has no such stage a bead can be made in: the refusal is
  logged as `make_refused`, of the class `attention`, beside the
  `hook_rule` event. What the yard
  did itself, and what happened before the hook was last saved, makes
  nothing. A `token` hook's line files a delivery that belongs to no
  thread as a standalone bead: the delivery's own request fields, every
  other top-level string of its JSON body but the routing names (`train`,
  `repo`, `number`, `head_sha`, `head_repo`), ride as `hook.<key>` metadata
  and are listed in the body, so `--makes "event=box verb=create ->
  box:requested@webapp"` files a `box` bead in `requested` from a
  delivery of `{"type":"box","verb":"create",...}`. The line must name
  the depot, since a threadless delivery names no repository; the bead
  has no `tracks` link, is titled by the delivery's text, and is filed
  on the operator's whatever actor the sender claims. A resend under the
  same `Yard-Delivery` id is the receiver's duplicate and files nothing
  twice. A body that is no JSON object, or one missing a key the line
  names, matches no line and files nothing.
- **A CI run on a branch** that belongs to no pull request has the thread of
  the branch, `<owner>/<repo>@<branch>`, at `<repository URL>/tree/<branch>`:
  the same thread and URL for every run of the branch, so a line makes one
  bead of the failed runs of a branch, `--makes "event=workflow_run
  conclusion=failure head_branch=main -> task:new"`. The next failed run
  finds the bead while it is open and makes none, and the run after its
  close makes the next. The bead is titled `CI on <owner>/<repo>@<branch>`
  and its body ends with the run's own page, not the branch's. A table's
  `says` line for the run moves the bead without the pull request's head
  being asked, as the branch has none. A run on a fork's branch is on no
  thread of the repository, whatever the branch is called, and makes
  nothing; a run for a pull request keeps the pull request's thread.
- **A thread** is `<owner>/<repo>#<number>`, and `yardr thread open github
  --in <owner>/<repo>` names a repository where Linear names a team.

A signed GitHub comment that mentions the crew member's login, sent to the
isolated yard, was recorded as

```
    34 10-06 11:01:17  -        hook           hook:gh      action=created actor={id=jane name=Jane} deliveries={yardmaster=mentioned} … fields={action=[created] event=[issue_comment] label=[bug] number=[7] repo=[acme/webapp] state=[open]} hook=gh mentions=[yardmaster-bot] message=991 new_mentions=[yardmaster-bot] source=github text=@yardmaster-bot can you look? thread={id=acme/webapp#7 title=Retries drop the last attempt url=https://github.com/acme/webapp/issues/7} type=issue_comment …
```

### Telegram, where it differs

The same steps, with these differences (`yardr pack show telegram` walks
them whole):

- **The pack tells and hears.** Applying it declares the `telegram`
  source with a `notify` key, a `hook` key (verify `secret`, `bodies:
  unique`, `normalize`, `keys: [chat]`), `thread.reply`, `thread.posted`
  and `thread.read`, and a `hear` key with `from: operator`. Telling a
  member through telegram tells them, but hears nothing back.
- **The hook.** `yardr hook add telegram --source telegram --secret-file
  ~/.yardr/telegram.secret`. Telegram authenticates with the secret
  itself in `X-Telegram-Bot-Api-Secret-Token`, compared as it arrives,
  not with an HMAC of the body; it sends no timestamp, so a test
  delivery needs none. The declaration names `setwebhook` under
  `hook.register`, so the hook card registers the webhook: the yard
  makes the secret and calls `setwebhook` (setWebhook) with the `hook_public_url`
  URL; by hand the operator tells Telegram once instead, on a port it
  takes (443, 80, 88, 8443).
- **The credential** is the bot's token twice: the source's env file
  holds `TELEGRAM_BOT_TOKEN` for telling, and the answering crew
  member's env file holds the same value for replying, since there is
  one bot for the whole yard and it speaks as the bot. Rotation rewrites
  both files.
- **No identity.** The API sends the bot only others' messages, so there
  is no echo of its answers to exclude, and no crew member sets a
  telegram identity.
- **The subscription** is the yardmaster's alone: `yardr subscribe
  yardmaster telegram --scope all --where type=message --where
  chat=<his chat id>`. `all` is needed, not `involved`: the first
  message mentions nobody. The user himself subscribes to nothing.
- **A thread** is the chat id. `yardr reply <seq>` posts `sendMessage`
  to the chat the delivery came from, or with `--file` the pictures
  and documents below. A reply run again after its
  answer was lost posts twice, since the service takes no idempotency
  key; `yardr thread show` on a telegram delivery shows nothing, since
  a bot cannot read a chat's history; and `yardr reply --to` is refused
  for telegram. There is no `thread open`, no status and no bead link
  to a chat.
- **What the chat is.** The yardmaster treats a line from the operator's
  chat as his word, as his mail. A number in the chat answers no ask;
  approving a run and giving a secret stay the operator's at the
  terminal. `yardr user set <name> notify=` ends telling alone: the
  chat is heard while the address is set, whatever tells them, and
  ending hearing takes `deleteWebhook`, or the address out of the env
  file.

- **Pictures and files.** A photo (its largest size), a document, a
  voice note, a video or an audio arrives as its bracketed note
  (`[photo]`, `[document: <name>]`) with the file's ref in the
  envelope. Once the delivery is kept and answered the yard fetches
  it with the source's `TELEGRAM_BOT_TOKEN` (`getFile`, then the file)
  into the blob store, at most the yard's `blob_max_bytes` and the
  pack's 8 MiB, whichever is lower (the Bot API serves a bot files up
  to 20 MB). The inbox shows it under the delivery's line, `◆ 1 image:
  <path>`, and `yardr hook show <seq>` after the body; one over the
  limit shows `not taken:` with its size. A sticker, an animation and a
  video note stay their note alone. A stranger's file is never
  fetched: the delivery is dropped before it is kept.
- **Who sees the files.** Whoever sees the delivery: a hook delivery
  reaches every device user (it is the yard's, not one user's letter),
  so a device and the web view are served a file a kept delivery names
  by its hash, which only someone with the bytes can know. The file
  goes when the delivery goes with the log (`prune`), and not before.
- **How a device learns of them.** The files land after the delivery and
  its notification, so a device reads them by the call `DeliveryFiles`
  with the delivery's seq, which names each by its hash, and reads again
  when the `hook_files` event of that seq arrives: the yard logs it in
  the write that records the files, and sends it to every device as it
  sends the delivery ([api.md](api.md#deliveryfiles)).
- **What is not fetched.** A fetch cut short by a server restart is
  not tried again: the delivery keeps its ref and its note. Nor is one
  with no credential set, nor one where the delivery carried none; such
  a delivery has no `hook_files` event, and `DeliveryFiles` reads none for
  it. No total volume or per-sender bound is set; the chat is one
  person's, at a person's pace.
- **Pictures out.** `yardr reply <seq> - --file shot.png`
  sends a PNG, JPEG, GIF, WebP or BMP by `sendPhoto` and anything else
  (an SVG, a PDF, a log) by `sendDocument`:
  the text, the words on stdin, is the caption of the first where it fits
  in 1024 characters, else it goes first as its own message and the files
  follow without one, several one after another. A tell with a picture
  goes through `notify.image` the same way, one picture per tell
  ([users.md](users.md#told-outside-the-yard) says which tells carry
  one); one without goes through the `message` call, and so does one
  whose photo Telegram refuses (over 10 MB, or 10000 pixels in width and
  height together): a reply to a photo it refuses fails with Telegram's
  words.

A line from the phone, sent to the isolated yard, was recorded as

```
    42 10-13 17:40  hook (all) telegram message create by mckean on "mckean": Can the shed land on Monday?
```

### Slack, where it differs

The same steps, with these differences (`yardr pack show slack` walks
them whole):

- **The pack hears and answers.** Applying it declares the `slack`
  source with a `hook` key (verify `hmac-sha256` over
  `v0:{timestamp}:{body}`, `bodies: repeat`, the `url_verification`
  challenge, `normalize`, `keys: [channel, team, sender, room]`),
  `thread.reply`, `thread.posted` and `thread.read`, and a `hear` key
  with `by: identity`. It names no `notify` key, no `link` and no
  status: the wish is to write to the yard, not to be told by it.
- **The hook** is two cards on one bead, the secret first: `yardr bead
  ask <id> "…" --secret-hook slack --source slack --context "…"` pastes
  the signing secret where Slack shows it, typed unseen, and the yard's
  server writes it to `<home>/hooks/slack.secret` (mode 0600), which
  the hook then names; `yardr bead ask <id> "…" --run "hook add slack
  --source slack --unknown unknown" --context "…"` stands `POST
  /hooks/slack` up, verified with that secret, and sends nothing
  anywhere: Slack takes its Request URL only in its own configuration.
  `--unknown unknown` holds a sender's messages on the operator's
  mapping card until answered; a user pick routes them as that user,
  and `Let in, once` routes them as outside without mapping the sender
  and closes the card, so the sender's next message offers a fresh card.
  The app is created from the pack's manifest,
  which carries no event subscriptions and no Request URL, and once the
  hook stands the operator turns Event Subscriptions on in the browser
  with the hook's URL and the two bot events `app_mention` and
  `message.im` — never `message.mpim`, so no group conversation the
  bot was never addressed in reaches the yard. Slack's
  `url_verification` challenge to that URL is answered from the hook.
- **The credential** is the bot's token twice: the source's env file
  holds `SLACK_BOT_TOKEN` for the check and post calls, and the
  answering crew member's env file holds the same value for replying,
  since there is one bot for the whole yard and it speaks as the bot.
  The crew member's copy is given on a card (`yardr bead ask <bead>
  "…" --secret slack --for-crew yardmaster --context "…"`), and takes
  effect when it next starts, so restart it. Rotation rewrites both
  files.
- **The identity** is the bot's `user_id` with its `bot_id` as the
  key, both read from `yardr source check slack`, which prints the
  check call's selected fields: `yardr bead ask <bead> "…" --run "crew
  identity yardmaster --source slack --id <user_id> --key <bot_id>"`.
  Mentions arrive as member ids, matched exactly; the key is what the
  echo of the bot's own post may carry alone, and with both set the
  yard drops its own posts before hearing, with no mapping card. A
  check that prints no `user_id` line has not shown whose token it
  holds, and setup stops there.
- **The subscriptions** are the yardmaster's alone: `yardr subscribe
  yardmaster slack --scope mentions --where type=message`, and `yardr
  subscribe yardmaster slack --scope all --where type=message --where
  room=dm`. `mentions` wakes the bot when it is addressed; a follow-up
  in a thread it answered in reaches it only when it mentions the bot
  again. `room` is `dm` for a one-to-one direct message, heard whole,
  and `channel` for everything else. Group direct messages are
  untested: whether Slack delivers a mention in one as `app_mention`
  is checked in a live trial, and until then only channel mentions and
  one-to-one direct messages are promised.
- **A thread** is `<channel>:<parent ts>`. `yardr reply <seq>` posts
  `chat.postMessage` in the thread the delivery stood in, and `yardr
  thread show <seq>` lists it oldest first through `conversations.replies`,
  following its cursor to the end, on the same bot token. A reply run
  again after its answer was lost posts twice, since the service takes
  no idempotency key; where Slack refuses the read the command fails
  with Slack's reason rather than showing an empty thread; and `yardr
  reply --to` stands with `thread show`, since it finds the message to
  answer under by listing the thread. There is no `thread open`, no
  status and no bead link to a thread.
- **Who is heard** is who each speaker is. A delivery whose sender is a
  user's `slack` identity arrives named as them, and a subscription may
  filter `--where speaker=<user>`; an unmapped sender is what the
  hook's word says, a mapping card under `unknown`, heard as an
  outsider under `allow`, dropped with no card under `ignore`. Picking
  nobody on the card ignores that sender; `yardr source show slack`
  and `yardr source ignore slack` list the ignored ids, and `yardr
  source ignore slack --rm <id>` hears them again. A delivery whose
  sender is a crew member's identity in the source is the yard's own
  post coming back: dropped under any word, with no mapping card.
  Edits and deletes in direct messages arrive under `message.im` and
  are ignored.
- **What a line weighs.** A mapped member's line is their word for
  conversation and filing, as their mail is, and never answers an ask;
  an outsider's is information, to be read and answered only. What the
  yardmaster may tell an outsider in a shared channel is what their own
  question is about, and what a mapped user present asks to have
  explained from what that user may see; anything beyond waits for a
  word at the yard. Slack lines reach the line session, which files
  with `--for` for the member whose line it was. Approving a run and
  giving a secret stay the operator's, and no credential is ever pasted
  into a chat.

A mention, sent to the isolated yard, was recorded as

```
    41 10-19 14:50  hook (mentioned) slack message create by ada (U012AB3CD): <@U0BOT7> can the shed land on Monday?
```

### The checks, in one place

| what | how |
|---|---|
| the listener | `yardr doctor`: `hooks: the server takes webhooks on <addr> (…)`, or why it does not ([doctor.md](doctor.md)) |
| the hook and its secret file | `yardr hook list`: STATE `ok` or `blocked: <reason>` |
| the service reaches the yard | `yardr hook list`: LAST ACCEPTED and LAST REFUSED; `yardr events` for `hook` and `hook_rejected`; doctor notes a hook whose last delivery was refused |
| the credential's names | `yardr crew show <name>`: `<source> credential: …`. Whether the service takes it shows at the first `yardr thread show <seq>` |
| identity, grants, subscriptions | `yardr crew show <name>` |
| the stage map | `yardr hook list` (MOVES ISSUES), `yardr hook show <name>` |
| a pack's view of all of it | `yardr pack check` and `pack apply` print, per source a pack names, what the yard has and lacks: hook, secret file, identity, credential, listener ([packs.md](packs.md#how-a-pack-maps-to-the-yard)) |
| a declared source and its credential | `yardr source show <name>`: `credential: <NAME> is set in <file>`, or why not; `yardr source check <name>` reads it as a call would. `yardr doctor` says it of every declared source: `sources:` and its notes, and with `--try` each one's check call |
| who may call a declared source | `yardr source show <name>`: `may be called by …` under each call; doctor notes a source nobody is granted. For what a pack asks: `yardr pack check`, `calls of source <name>, for …` |
| that a user is told | `yardr source check <source> --user <name>` sends one test message; `yardr user show <name>`: `last told: …`; doctor notes a user who was not told ([doctor.md](doctor.md)) |

## Adding a service

What someone writes to make yardr carry a new service is a pack of kind
source — a declaration, the scripts it names, and setup notes — and no
Go. A new one starts from the template, `packs/template`: copy the
directory, rename its service (the skeleton calls it `acme`) in the
files' names and text, and drop the keys the service does not need. The
skeleton names the keys [pack-api.md](pack-api.md#the-files) lists
for a declaration in a pack of kind source, each with when it stays;
its command stubs show each stdin and stdout shape; its setup notes
are the checklist to fill for the service.
A test holds it to the reader — it copies the template, renames it, and
applies the copy in a scratch yard — so what the skeleton shows is what
the reader takes. [A declared source](#a-declared-source) with `notify`,
`hook`, `thread` and `link` keys receives, replies, opens, sets, moves
and links, as `packs/github/github.yaml` does for GitHub and
`packs/linear/linear.yaml` does for Linear. What is still Go, in yardr's
own repository, is `token`, yardr's own source, which receives and nothing
else.

**The least: receive.** A file that declares the source: its `hook`
(`verify`, `bodies`, `normalize`, `keys`). From `yardr source add` on
`yardr hook add --source <source>` and `yardr subscribe <name> <source>`
take the name, deliveries become events, and subscriptions with `type` and
`action` filters work. What the normalize command must get right for the
rest to work: `actor.id`, `mentions`, `assignee` in the form an identity
will be given in; `thread.id` the same for every delivery of one issue;
`entity` wherever the service re-sends a text.

**Then, each as wanted:**

| to have | give the file | and also |
|---|---|---|
| `--where` on the source's own fields | the keys under `hook.keys` | nothing: `subscribe` reads the declaration; the help of `yardr subscribe` names the common keys by hand |
| `yardr link` | the `link` key: the host, an expression that reads an issue from a URL's path, and the fixed and thread templates over its groups | nothing: `LinkSources` finds it |
| `yardr reply`, `thread show` | the `thread.reply`, `thread.posted` and `thread.read` commands | the credential's variable under `credential.env`, which `crew show`, the pack plan and the daemon read |
| `yardr thread open` | the `thread.open`, `thread.opened` and `thread.place` keys | nothing |
| `yardr thread state` | the `thread.state` command and `thread.states`: what `started`, `review` and `done` mean there | nothing |
| a stage map | the status keys and the `link` key | nothing: `hook add --map`, the pack's `sources` and the daemon ask `ForwarderFor` |
| `yardr source check` making its check call | the `check` key, naming a read call that takes no required parameter | nothing: `source check` reads the declaration (below) |
| telling a user through the service | the `notify` key: the update call that tells, and which of its parameters take the address, title, text and link; `image`, the command that tells with a picture | an update call with a string parameter for each |
| hearing only set addresses back | the `hear` key | the `notify` and `hook` keys, and the field under `hook.keys` |
| reading a pull request and its CI back | the `events` key: the run and head types, and the `pr` and `ci` commands | nothing: the daemon reads a train's pull request and CI through them, and the brief carries the snapshot |

**The envelope a normalize command prints.** Its stdin is
`{"headers": {...}, "body": ...}`: the delivery's headers, each with its
values, and the body as JSON, or as text when it is none. With scheme
`secret` the header that holds the secret is taken out first. Its stdout
is one JSON object, the envelope, at most 64 KB, exit 0, within 30
seconds; anything else is a failure, and the delivery is refused with
400. Of the object only `type` is required; the core sets `source` and
`delivery` itself, collapses whitespace in `text` and cuts it to 500
characters, gives `mentions` an empty list where none was printed, and
derives `train` from `head_branch` where the branch is a train's branch
and the envelope carries no train. What `fields` may carry is the
file's `hook.keys`, which `--where` may then name:

```json
{
  "type": "issue",
  "action": "create",
  "actor": {"id": "u-1", "name": "Jane"},
  "thread": {"id": "acme-1", "title": "An issue", "url": "https://acme.example.com/acme/issue/acme-1"},
  "mentions": ["u-2"],
  "assignee": "u-3",
  "text": "an issue was created",
  "url": "https://acme.example.com/acme/issue/acme-1",
  "message": "m-1",
  "parent": "m-0",
  "entity": "acme-1",
  "fields": {"team": ["acme"]}
}
```

A delivery that carries files names them under `files`, each
`{"id": "<the source's id>", "name": "<as sent, where given>", "type":
"photo|document|voice|video|audio", "size": <bytes, where the service
says>}`: refs read from the body, never the bytes, since normalize is
handed no credential. `thread.file` fetches them after the delivery is
kept.

`type` is what happened to what, by convention lowercase; `action` what
of it happened. `actor.id` is what an identity is matched against.
`thread` is what the delivery belongs to, empty for what belongs to
nothing. `mentions` are the ids or keys the text mentions, in the form
an identity takes. `message` is the comment the delivery is, and
`parent` the comment it answers, so a reply lands in the same
conversation. `entity` is what the text belongs to where the service
sends that text again with every change. What the skeleton's stub
prints is this shape with fixed values.

**The check a source declares.** `check` names the call `yardr source
check <name>` makes: a read call that takes no required parameter.
What the command does, in order: it reads the source's own env file as
a call would and reports the credential — the names set and missing,
and with a mint whether a mint command is set; it mints where the file
sets the mint's names and makes the check call with the credential, and
fails unless the service answers 2xx; where the check call names
`answer.select` it prints each selected top-level field the answer
carries, one per line under the status line, and with `--json` carries
the same object under `answer`; it says of every command the file
names beside the check call — thread's, hook's, events', and each
call's build — whether its
program is there and can be run, and fails when one cannot, running
none. A file that names no check call is sent
nothing: its credential is looked at and that is all. With `--user` the
command tells that user through the source instead: one real test
message. So a new source declares a `check` with a ping-like read call
behind it, and ships every command's program in the pack; `source
check` then passes before the first delivery, and says which program or
name does not hold when it does not.

**A call may build its body with a command.** Where a fixed body cannot
say the request — ranges worked out from a read first, a message
encoded — the call names `build` beside its `request`: an argv whose
program is a file beside the declaration's file, copied into
`~/.yardr/sources/<name>/`, hashed and drift-checked as every command's
file is. The call keeps its kind, method, path, query and answer; at
each call the yard runs the command with the parameters on stdin and
the credential in its environment, and sends the JSON it prints. The
command may make its own requests to the service with the credential
while it builds, and a refusal from it sends nothing. `yardr source
show` prints the build under its call, the pack plan hashes and diffs
the script, and the apply card says the call builds its body with a
command run with the credential and free to make its own requests. The
check, notify and hook-register calls name no build: those paths send
the template.

Two more places know sources by name: `internal/standin`, the stand-in for
the services' APIs that the tests and an isolated yard use, and the docs.

**The tests a source pack has**, in `internal/hooks` unless said: the
goldens over recorded payloads in `testdata/` and `testdata/golden/`
(`TestGitHubPackNormalize`, `TestGitHubPackThread`, `TestGitHubPackForward`,
`TestGitHubPackPR`, `TestGitHubPackCI`), which fail when the pack's
envelopes or the requests its scripts make drift; the lookups
(`TestGitHubPackLookups`, `TestGitHubPackLinkFixedForm`); the fields
against the where keys (`TestGitHubPackFieldsAreWhereKeys`); and in
`internal/dispatch` the moves (`TestIssueFollowsItsBead`,
`TestIssueOnlyMovesForward`, `TestIssueNeedsSetState`). The yard's own
(`token`) has `TestTokenSource` and
`TestTokenDedupesOnDeliveryIDOnly`. The skeleton a new pack starts from
has its own in `internal/pack`: the template test copies
`packs/template`, renames it, and applies the copy in a scratch yard,
and runs each stub in shape.

The outbound tests run against the stand-in the test starts, reached
through `YARDR_SOURCE_BASE`.

**What checks that a file conforms.** The reader, when the file is added:
a key it does not know, a command that is no list of words, and
a name of yardr's own are refused with the path. For the scripts nothing
does: a command with a wrong shape fails at the call, against the real
service or the stand-in. A capability is found by its keys at run time,
so a source without them lacks it, and stays silent about the rest. There
is no suite that takes any pack and checks its behaviour: each pack has
tests of its own, over its own payloads.

## Gaps

What is missing, as found while writing this page. A list to decide on, not
a design.

1. **No single unit says "this yard has Linear".** An integration is a
   setting, a secret file, a hook, an env file, an identity, two grants,
   subscriptions and a map, set by eight or so commands in an order the
   commands do not enforce, plus a webhook made by hand in the service.
   Nothing lists the integrations of a yard; `yardr hook list` and `yardr
   crew show` each show a part.
2. **A pack carries part of it, and only part.** From `pack.yaml`: a
   crew member's unfiltered subscription to a source
   (`crew.<name>.subscribe.<source>: <scope>`), the request that the
   crew member may open threads there (`open_threads`, which the import puts
   to the operator), and the stage map with the crew member it acts as
   (`sources.<source>`), and the declaration of a declared source
   (`sources.<name>.declaration`), with the calls of it the pack asks that
   its groups and crew may make (`calls`, put to the operator too). A pack does not make the hook, the secret file or
   `hook_addr`, does not set the credential or the identity, cannot ask for
   `set_state`, and carries no subscription with a `--where`. For a
   declared source it brings the file and nothing else: not the
   credential's file, not which user is told through it. Its plan is
   the nearest thing to a check of a whole integration, and it exists only
   for a source a pack names.
3. **Three ways a service signs are Go code inside yardr.** A new
   service is a pack outside the core, a declaration, scripts and setup
   notes and no release, when it signs the body (or a template over the
   timestamp, the delivery id and the body) with an HMAC over SHA-256,
   SHA-1 or SHA-512, or sends the secret itself in a header. What is still
   an adapter of its own is `token`, yardr's own source. A service that
   signs another way wants a new `hook.verify` scheme or key, which is a
   change to `internal/hooks` and a release. The ways met so far:
   - a public-key signature, as Discord's Ed25519 over the timestamp and
     the body;
   - a signed string that holds the request's URL or its form fields,
     beside the body or instead of it, as Twilio's, Trello's and
     Mandrill's HMAC-SHA1 do;
   - a timestamp carried inside the signature header, as Stripe's and
     Calendly's `t=…,v1=…`.
4. **A user is told through a declared source only, and only by one HTTPS
   request.** No compiled adapter tells a user, and nothing that is not a
   JSON request does (a desktop notification, SMTP). What is told is fixed:
   no setting for the parts of the inbox, the amount of text, the grace,
   the bound or quiet hours, no link, one source per user
   ([above](#what-is-not-there)).
5. **No command says what a source can do.** The capability table above is
   read from the code. A user learns that `token` cannot be answered in
   from the refusal. The two kinds of source also have two lists: `yardr
   hook list` for what is heard, `yardr source list` for what is declared.
6. **Nothing checks a crew member's credential until it is used.** `yardr crew show`
   and the pack plan read the names in the `env_file`. A wrong or expired
   token shows at the first reply, and for a stage map as a move that
   fails and is retried (`yardr hook show <name>` has the last error).
   A declared source's own credential is checked: `yardr source check`,
   `yardr doctor --try`, and doctor's note on a last call answered 401 or
   403. A crew member's is not, in any source.
7. **The identity is looked up by hand.** yardr holds the token and could
   ask the service who it is; no command does, and a wrong id fails
   silently: the crew member is never mentioned and hears its own actions.
8. **No test delivery.** Checking a hook end to end takes a real event in
   the service or a body signed by hand, as in step 9.
9. **yardr does not know its public address** and makes no webhook in the
   service. The URL to paste is assembled by the person, and a webhook
   pointing at a yard that is gone stays in the service.
10. **Where keys are a list beside the script that sets them.** A field a
    normalize command sets is filterable only when the declaration names
    it under `hook.keys`: `--where` on a field the list leaves out is
    refused. A test holds the two together for what the fixtures cover
    (`TestGitHubPackFieldsAreWhereKeys` in `internal/hooks`,
    `TestAdapterFieldsAreWhereKeys` for the yard's own): it normalizes
    every payload under `internal/hooks/testdata` and fails on a field the
    source's where keys leave out. A field no fixture's event sets is not
    checked, so a new event wants a fixture.
11. **Conformance is not checked** beyond the three methods of `Adapter`
    (above).
12. **A reply needs a delivery.** `yardr reply` answers a hook event by its
    seq. A crew member cannot comment on an issue that sent the yard nothing,
    though it can set the status of one it follows.
13. **The states are three words.** `started`, `review`, `done`, moved
    forward only. A service's other statuses (cancelled, blocked, a
    backlog) cannot be named by a map or by `yardr thread state`. On
    GitHub, which has two statuses, `label:<name>` adds a label in place
    of one; no label is ever removed.
14. **A map is per source.** Its lines are `<bead type>:<stage>`, and it
    acts on every tracked issue of the hook's source: a map per team or
    per repository is not expressible. Every hook of the source that has a
    map queues its own move, and with several hooks of one source a pack
    puts its map on the first by name.
15. **The envelope has service-specific parts.** `delegate` is Linear's.
    GitHub events yardr does not read (a review comment) are recorded with
    `event`, `action` and `repo` and no thread, so nobody is mentioned by
    them.
16. **The stand-in knows part of the services.**
    `contrib/isolated-yard/sources` answers reading an issue and setting
    its status, for both services, and takes a message for a person as
    Pushover does. A reply and an opened thread cannot be
    tried in an isolated yard: they are covered by the tests' fake servers
    and by the real service only.
