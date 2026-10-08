# Integrations

An integration is a service outside the yard that the yard hears and, where
the service allows it, speaks in: Linear, GitHub, and a service a file
declares, such as Pushover. This page says what one is
made of today, how one becomes part of a yard, with Linear as the example
from nothing to a merged bead closing its issue, and what someone would
write to add a service. It ends with what is missing.

It describes what is built. The other pages go deeper and are linked where
they do: [subscriptions.md](subscriptions.md), [links.md](links.md),
[crew.md](crew.md), [packs.md](packs.md),
[configuration.md](configuration.md).

## What an integration is

Two things, kept in two places:

- **An adapter**: Go code in `internal/hooks`, compiled into yardr. One per
  service, registered by name in the map `hooks.Adapters`. The name is the
  **source**: `linear`, `github`, `token`. yardr has these three and no way
  to load another. Beside them a yard has its **declared sources**: a
  service added by one YAML file and no Go ([below](#a-declared-source)),
  which today can do three things of this list, the last three.
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

| capability | interface (file) | `linear` | `github` | `token` | a declared source |
|---|---|---|---|---|---|
| receive | `Adapter` (`hooks.go`) | yes | yes | yes | no |
| identify | none: read from the envelope by the store | mentions, assignee, delegate | mentions, assignee | never | no |
| link | `Linker` (`issue.go`) | yes | yes, issues only | no | no |
| reply, read a thread | `Replier` (`reply.go`) | yes | yes | no | no |
| open a thread | `Opener` (`open.go`) | yes | yes | no | no |
| set a thread's status | `Stater` (`state.go`) | `started`, `review`, `done` | `started`, `done` | no | no |
| move with a bead's stage | `Forwarder` (`forward.go`) | yes | yes | no | no |
| tell a user outside the yard | `Notifier` (`notify.go`) | no | no | no | yes, with a `notify` key in its file |
| say what a harness kind's subscription still carries | none: a command the source's file names, run by `internal/limits` | no | no | no | yes, with a `limits` key in its file |
| be read by a session or a crew member | none: a read call of the source's file, made by the server (`store.CallSource`) | no | no | no | yes, for whoever the operator granted it ([below](#a-call-by-a-session-or-a-crew-member)) |
| be updated by a session or a crew member | none: an update call of the source's file, made the same way | no | no | no | yes, for whoever the operator granted that call by its name ([below](#an-update)) |

The interfaces build on each other: an `Opener` and a `Stater` are each a
`Replier`, and a `Forwarder` is a `Stater`. `token` is the smallest
integration there is: it receives and nothing else.

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
  of the delivery (`""` when it has none). Linear and GitHub sign the body
  with HMAC-SHA256 (`Linear-Signature`, `X-Hub-Signature-256`); Linear's
  signed `webhookTimestamp` must also be within a minute of the yard's
  clock. `token` compares a bearer token.
- `Normalize` turns the verified body into the envelope.
- `Bodies` says how far a body identifies a delivery, which decides the
  dedupe keys below.

*The envelope.* What routing reads, whatever the source:

| field | what it is |
|---|---|
| `source` | the adapter's name |
| `type`, `action` | what happened to what: `issue` `create`, `comment` `update` in Linear; the event and its action in GitHub (`issue_comment` `created`) |
| `actor` | who did it: `id` and `name`. The id is what an identity is matched against |
| `thread` | what the delivery belongs to, as `id`, `title`, `url`: the issue a comment is on, or the issue itself. Empty for what belongs to nothing (a CI run on no pull request, a `token` message) |
| `mentions` | the ids or keys of the people the text mentions |
| `assignee`, `delegate` | who the issue is assigned to, and (Linear) the agent it is handed to while a person stays the assignee |
| `text` | the start of the delivery's own words, whitespace collapsed, at most 500 characters |
| `url` | the link to what the delivery is |
| `delivery` | the service's id of the delivery |
| `message`, `parent` | the id of the comment the delivery is, and of the comment it answers, so a reply lands in the same conversation |
| `entity` | the id of what the text belongs to when the service sends that text again with every change (an issue's description, an edited comment): the store counts only the mentions the entity did not have before, and the event carries them as `new_mentions` |
| `fields` | what the source says about where the delivery belongs, each a list of values: `team` (key and name), `project`, `label` in Linear; `repo`, `label`, `number`, `state`, `conclusion`, `head_branch` and more in GitHub |

`fields` are there for subscriptions: `--where team=YRD` matches a delivery
whose `team` field has the value `YRD`. `--where type=…` and `action=…` read
the envelope's own two fields. A where key that a source's deliveries never
carry matches nothing.

The event is the envelope plus `hook` (the hook's name), `deliveries` (the
crew members it reached, each with the reason), `notified` (the users) and
`new_mentions`.

*What is kept.* The event keeps the envelope, for as long as events are
kept (90 days unless `YARDR_EVENT_RETENTION` says otherwise). The raw body,
up to 1 MiB, is kept beside it under the event's seq for `yardr hook show
<seq>`, for 30 days (`YARDR_HOOK_PAYLOAD_RETENTION`). A larger body is
refused with 413
([configuration.md](configuration.md#retention-keeping-yarddb-bounded)).

*Dedupe.* A delivery is recorded at most once. Its keys are checked after
`Verify`, in the write that records it:

| `Bodies()` | source | keys |
|---|---|---|
| `BodiesUniquePerSource` | `linear` | the body's sha256, per source; the delivery id, per hook |
| `BodiesUniquePerHook` | `github` | the body's sha256, per hook; the delivery id, per hook |
| `BodiesRepeat` | `token` | the delivery id (`Yard-Delivery`), per hook, when the sender sets one |

A duplicate is answered 200 and leaves no event
([subscriptions.md](subscriptions.md#redeliveries)).

*A refused delivery* is answered with its status and logged as a
`hook_rejected` event with the hook's name, the reason (`unauthorized`,
`bad_request`, `too_large`, `blocked`, `secret`, `unknown_hook` among
them) and a count, at most one per name and reason in ten minutes
([subscriptions.md](subscriptions.md#a-refused-delivery)).

*Where a user sees it.* `yardr hook add`, `hook list`, `hook show <name>`,
`hook show <seq>` (the raw body), `yardr events`, `yardr doctor`'s hooks
lines.

### Identify

*What for.* To know which crew member a delivery is about, and which
deliveries are the crew member's own.

*No interface.* The adapter only fills `actor.id`, `mentions`, `assignee`
and `delegate`. The store compares them with the identity each crew member has
in the source:

```sh
yardr crew identity yardmaster --source linear --id <user id> --key <profile key>
```

An identity is an `id`, a `key`, or both. An id matches exactly; a key is a
name people type and matches in any case. Linear's adapter reports a
mention as a user id when the payload carries the rich text, and as the
profile key (`@yardr`) when it carries markdown only, so a Linear identity
wants both. GitHub's reports logins in lowercase, so a GitHub identity is
`--key <login>`; a GitHub App's is `--key '<slug>[bot]'`, and `@<slug>`
mentions it.

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

A user ([users.md](users.md)) has no identity in a source: only `--scope
all` reaches a person.

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
  environment of the calling process: `LINEAR_API_KEY`; for GitHub a GitHub
  App's `GITHUB_APP_ID` and `GITHUB_APP_PRIVATE_KEY_FILE`, else
  `GITHUB_TOKEN`.
- `Reply` posts in the thread of a delivery; `Thread` lists the thread's
  messages. `Open` starts a thread in a place (`Place` names it: a Linear
  team, a GitHub repository). `SetState` moves a thread to what `started`,
  `review` or `done` means in the source.
- `Posted` and `Opened` find what an earlier attempt left. Every reply and
  every opened thread is on record in the store with a key before anything
  is sent, and the key goes with the post (as the comment's or issue's own
  id in Linear, as an HTML comment in the body on GitHub), so a command run
  again after a timeout does not post twice.

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

*Where a user sees it.* `yardr reply <seq>`, `yardr thread show <seq>`,
`yardr thread open <source> --in <place>`, `yardr thread state <seq>
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
they name a source that can tell them:

```sh
yardr user set mckean notify=pushover env_file=~/.yardr/users/mckean.env
yardr source check pushover --user mckean     # one real test message
```

It is a capability of a source like the others, and a user names the
**source**: there is no separate thing called a notifier. A declared source
has it when its file has a `notify` key, which says which of its calls
tells a person and which of the call's parameters take the four values the
yard has for it: where the person is reached, a title, a text and a link. A
compiled adapter would have it by implementing `hooks.Notifier`; none does,
so no user can name `linear` or `github`, and the refusal lists the sources
that can.

Everything that decides whether and when a person is told ends at those
four values; sending them is one declared call. What is told, when, and
what leaves the machine is in
[users.md](users.md#told-outside-the-yard). [pushover.md](pushover.md) sets
one up from nothing.

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

yardr holds no logic of any provider. A declared source has the capability
when its file has a `limits` key: **a command**, and the harness kinds it
answers for, each with the provider's own name for it. Such a file needs no
`base` and no `calls`. This one is whole, for
[aiquokka](https://github.com/McKean/aiquokka), which reads the limits of
Claude, Codex, Kimi and others from the logins their CLIs store:

```yaml
name: aiquokka
about: Usage limits of the subscriptions the agents' CLIs are logged in to (github.com/McKean/aiquokka).
limits:
  command: [/Users/mckean/go/bin/aiquokka, "{provider}", --json]
  kinds:              # harness kind -> the provider's own name for it
    claude: claude
    codex: codex
    kimi: kimi
    grok: grok
    copilot: copilot
    kiro: kiro
```

```sh
yardr source add aiquokka.yaml
yardr source check aiquokka     # asks for each kind the file lists
yardr limits                    # every kind the yard runs on that a source lists
yardr limits kimi --json        # the report, for a program
```

Leave out of `kinds` the harnesses that are not logged in on the machine:
they have no answer, and `source check` fails for them.

- **The question** is a harness kind: a group's or a crew member's `kind`.
  Not a model: no provider answers by model. A kind no source lists has no
  limits known, which is the yard as it was before.
- **The command** is run by yardr itself, as the user yardr runs as:
  without a shell (each word is one element of the list), its stdin closed,
  in the yard's home, with the environment of whoever asks and nothing
  added, `{provider}` replaced by the name the kind maps to. It has 30
  seconds; then it and everything it started are killed as one process
  group. Name the program by its whole path: the daemon's `PATH` need not
  be your shell's.
- **Its answer** is exit status 0 and one JSON object on stdout, at most
  64 KB: `provider` (a string) and `windows` (a list), each window with a
  `label` (a string) and, where the provider knows them, `used_percent` (a
  number), `resets_at` (a time as RFC 3339 writes one), `used` and `limit`
  (whole numbers); `plan` (a string) beside them. Other keys are passed
  over. A `resets_at` before 2020 is read as not known: it is how a time
  that was never set gets printed.
- **What the yard reads from it itself** is one thing: a window is used up
  at `used_percent` 100 or more, or when `used` has reached a `limit` above
  0. Of several that are used up, the one that resets last is the window an
  agent waits for (`window` and `resets_at` in `--json`).
- **Anything else is no answer**: another exit status, nothing within the
  30 seconds, output that is not that object. The limits are then *not
  known*, with a reason in one line that ends with the last lines the
  command printed on stderr, never what it printed on stdout. `yardr limits`
  prints it and exits 1; `--json` has it as `limits_unknown`.
- **Whose subscription.** The answer is for the login the provider reads on
  this machine, the operator's. A group or crew member whose `env_file`
  gives its harness another account is not described by it.
- **When it is asked.** A provider is slow next to everything else here,
  may itself be bounded in how often it answers, and may renew a stored
  login as it reads one (aiquokka does, and writes the new one back where
  the CLI keeps it, which is why 30 seconds are never cut short). So the
  command is run in five places and no other. Three are **a person
  asking**: once per kind by `yardr limits`, once per listed kind by `yardr
  source check <name>`, and once per kind in use by `yardr doctor --try`;
  none of them remembers an answer. The fourth is the daemon, **when a
  harness has stopped on a usage limit**: while it raises a
  `session_harness_error` of reason `rate_limited`, for a session or a crew
  member, it asks for that harness's kind, and keeps what the provider said
  for 60 seconds in its memory, so agents that stop together are one run
  ([groups.md](groups.md#a-usage-limit-says-its-window-and-when-it-resets)).
  The fifth is the daemon again, **at the reset that event told of**: for
  an agent whose window was to reset within six hours it asks once more,
  a minute after the reset and just before it prompts the agent, with the
  same memory
  ([groups.md](groups.md#a-window-that-resets-within-six-hours-is-waited-for)).
  Nothing else asks: not a dispatcher pass that finds nothing stopped on a
  limit, not a session's start, not a timer, and not the hold that keeps a
  group from starting a further session with a model while one of its
  sessions waits for a reset: that is read from the waiting session, as
  `yardr session list` reads until when. `yardr doctor` without `--try`
  says which source answers for which kinds and runs nothing
  ([doctor.md](doctor.md#optional)).
- **Refused when the file is added**: a `command` that is no list of
  strings, has no `{provider}` among its arguments or has it in the
  program; a kind herdr starts no agent of; a provider's name that could
  be read as a flag; a kind another source of the yard already answers for
  (one source answers for a kind); `base`, `credential`, `check` or
  `notify` in a file with no `calls`.
- **It is the operator's.** Adding the file is refused from an agent's
  pane, as for every source, and here for a second reason: the yard runs
  what it names. For the same reason no pack brings one: a declaration in a
  pack with a `limits` key is refused by `yardr pack check`
  ([packs.md](packs.md#how-a-pack-maps-to-the-yard)).

### A declared source

A service outside the yard, added by one YAML file: its address, the
credential it takes, and the requests the yard may make of it, each with
its parameters. `yardr source --help` has the format, and
[pushover.md](pushover.md) a whole file.

```sh
yardr source add pushover.yaml                                    # or a pack brings it: sources.<name>.declaration
yardr source set pushover env_file=~/.yardr/sources/pushover.env  # where its credential is read from
yardr source list
yardr source show pushover
yardr source check pushover                                       # the credential, and the file's check call if it names one
yardr source rm pushover
```

- **The file holds no secret.** It names the variable the credential is in;
  the value is read from the `env_file` at each call (mode 0600, as every
  env file). So the declaration is kept in the store, shown, and carried by
  a pack ([packs.md](packs.md#how-a-pack-maps-to-the-yard)).
- **What a file can say**: JSON over HTTPS, and one thing that is no
  request: the command that says a subscription's limits
  ([above](#say-what-a-subscription-still-carries)). A request that must be signed,
  an answer in pages or a webhook to verify is not a declared source.
  Refused when the file is added: an address that is not `https://` (plain
  `http://` to this machine only, for a stand-in), a path that leaves the
  address, a credential in a path or a query. No redirect is followed.
- **Who calls one**: `yardr source check`, `yardr doctor --try` (the same
  check call, of every source), the daemon for a source a user
  names as their notify, and the server for `yardr call`, a call by a
  session or a crew member the operator granted it
  ([below](#a-call-by-a-session-or-a-crew-member)). A limits command is run
  by `yardr limits`, `yardr source check` and `yardr doctor --try`, and by
  the daemon for a harness that stopped on a usage limit, at the stop and
  at the window's reset. Nothing of a request is written to an event or a
  log.
- **Adding, setting and removing one is the operator's**, and refused from
  an agent's pane: a source is where a credential of the yard is sent, and
  its file may name a command the yard runs.
- **Its name is none of `github`, `linear`, `token`, `yardr`**: it is no
  hook source, so nothing subscribes to it and no hook receives it.

### A call by a session or a crew member

An agent reads from a declared source with `yardr call`, and writes to one
([below](#an-update)), when the operator granted its group or it the call. The worked example is a customer system
(`contrib/isolated-yard/sources/crm.yaml` is the file, for the stand-in
there):

```sh
yardr source add crm.yaml
yardr source set crm env_file=~/.yardr/sources/crm.env
yardr source grant crm read --group builders --group reviewers   # every read call
yardr source grant crm customer --crew yardmaster                # one read call
yardr source show crm            # each call, and who may make it
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
  credential from its `env_file` for that one call and keeps it nowhere, so
  it is in no pane's environment, no brief and no event. This is the one
  place the server speaks to a service. It is a line, not a wall: every
  agent runs as the same OS user and could read a 0600 file if it went
  looking. The request is made outside any store write and ends after 30
  seconds; yardr tries no call twice.
- **One credential a source, or two.** Whoever calls, the request goes out
  with the yard's credential for the source, not the caller's. A file that
  names a second variable (`credential.update_env`) has its update calls
  sent with that one, so the reads can run on a token that may only read;
  the `env_file` sets both, and `yardr source set` refuses one that does
  not.
- **A grant is a record of its own**, by source and grantee, not a key of
  a group's or a crew member's settings: `yardr group add` replaces a
  group's definition whole and a pack's import writes groups, so a grant
  kept there would be dropped by the one and could be carried in by the
  other. `read` is every read call of the source, one a later file adds
  included; `yardr source add` says who gets a call that is new or changed
  through it. A call's name grants that one call, and is the only way an
  update call is granted. Granting is the
  operator's and is refused from an agent's pane whatever `--actor` says;
  anyone may revoke. `yardr source show`, `yardr crew show` (`may call crm:
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
  session of its exec group and calls under that group's grants; a gate
  (`yardr depot check`) runs in no session and is granted nothing.
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
  redirect is followed: the 3xx fails the call.
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
  `session` and `group` or `crew`, `status`, `ms`, `bytes`, and `params`,
  the names of the parameters given. `status` is the service's HTTP status,
  or why nothing was sent or returned: `not_granted`, `from_peer`, `limit`,
  `bad_param`, `no_credential`, `refused`, `unreachable`, `timeout`,
  `redirect`, `too_large`, `not_json`. The value of a parameter is kept
  (under `values`) only when the file marks it `log: true`, for what an
  audit asks by. `attempt` is there, from 2, for an update sent again with
  the key of a try before it. Never kept: the credential, the request, the
  answer, the address with its values, the key.
- **What is wrong with one** is in `yardr doctor`'s `sources:` line and its
  notes: a credential file that is not set, missing, open to others or
  without the name; a source whose last answered call the service refused
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
  making the same call with the same values again, after a try that did not
  end in a success (a timeout, a 500, a connection that broke), is sent
  with the same key, so a service that took the first writes nothing more.
  After a success the same call is a new one. yardr itself tries nothing
  twice: the caller runs the command again. A key nobody tried again is
  dropped after a day, and the store keeps a hash of the values, not the
  values. Without the key in the file a second try is a second write.
- **In the record** the event has `kind: update`. A note's text is free
  text and is not marked `log: true`.

### What is not there

- Telling a user through Linear or GitHub (a DM, a mention): no adapter
  implements `Notifier`.
- A way to be told that is no HTTPS request with a JSON body: a desktop
  notification, mail by SMTP, a program of the operator's. The place for
  one is behind the same four values; none is built.
- Per user: which parts of the inbox are told, how much of the text, quiet
  hours, the grace and the hourly bound (all fixed), a link a phone can
  open, several sources at once. For a crew member or a group: nothing.
- Events in from a declared source (the key `events` is kept for them).
- For a call by an agent: opening a source to the beads of one linked yard, a person who
  confirms an update before it is sent, a grant for a user who is not the
  operator, a setting for the 30 seconds a call may take, calls served as
  tools (MCP). An update of a service that takes no idempotency key is
  written twice when it is run again after a timeout.
- Limits, beyond showing them and saying them in the event of a harness
  that stopped on one: a stopped agent is not prompted again when the
  window opens, and a group starts a session whatever a window says. Also
  not there: a second way to an answer (a
  declared HTTPS call, an adapter in Go), limits by model, the limits of an
  account that is not the operator's.

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

The listener serves `POST /hooks/<name>` and the yard's peers, and nothing
else. The service must be able to reach it: the yard needs an HTTPS URL
that arrives at `hook_addr`'s `/hooks/<name>`. How that URL comes to be is
outside yardr; let what stands in front pass `/hooks/` and nothing more.
yardr does not know its public address and never calls the service to
register a webhook ([configuration.md](configuration.md#settings)).

### 2. The hook and its secret

*Anyone.* The secret the service signs with goes in a file, mode 0600, and
the hook names the file:

```sh
printf '%s' '<signing secret>' > ~/.yardr/linear.secret
chmod 600 ~/.yardr/linear.secret
yardr hook add linear --source linear --secret-file ~/.yardr/linear.secret
# hook linear saved: POST /hooks/linear (linear)
```

A file open to others is refused (`… is open to others (mode 0644); chmod
600 it`). The file is read on every delivery, so rotating the secret is
rewriting the file ([subscriptions.md](subscriptions.md#the-secret-file)).
One hook serves every crew member.

*(in Linear)* Settings → API → Webhooks: the URL
`https://<public host>/hooks/linear`, the events Issues and Comments. Its
signing secret is what goes in the file.

Check: `yardr hook list` shows the hook with STATE `ok`, and `-` under LAST
ACCEPTED until a delivery came.

### 3. The crew member's credential

*Anyone, for the file; the token is made in Linear.* What the crew member
speaks with goes in an env file of its own, never in the store:

```sh
echo 'LINEAR_API_KEY=<token>' > ~/.yardr/crew/yardmaster.env
chmod 600 ~/.yardr/crew/yardmaster.env
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

### 4. The crew member's identity

*Anyone.* The id and the profile key of the user the token acts as:

```sh
yardr crew identity yardmaster --source linear --id <user id> --key <profile key>
# SOURCE  ID                                    KEY
# linear  11111111-2222-4333-8444-555555555555  yardr
```

Both are looked up in Linear by hand; yardr has no command that asks the
service who a token is.

### 5. The grants

*The operator only.* Answering needs none. Opening issues and setting
their status do, and the stage map of step 7 needs `set_state`:

```sh
yardr crew set yardmaster open_threads=linear set_state=linear
#   open_threads: (not set) -> linear
#   set_state: (not set) -> linear
```

From anyone else: `open_threads: only the operator (jo) may let yardmaster open
threads in linear; this is mckean`.

### 6. Subscriptions

*Anyone, for a crew member.* What of the source reaches the crew member's inbox:

```sh
yardr subscribe yardmaster linear --scope involved      # mentions, what is assigned or delegated to it, its threads
yardr subscribe yardmaster linear --scope all --where type=issue --where action=create --where team=YRD
```

The second is "a new issue of team YRD reaches the yardmaster", whoever it
mentions. The where keys of a hook source are `type`, `action`, `team`,
`project`, `repo`, `label`; GitHub has more
([subscriptions.md](subscriptions.md#github)). Linear's deliveries carry
`team` (key and name), `project` and `label`. A key that is none of the
source's is refused.

### 7. The stage map

*The operator, or the crew member's own session.* Which stage means which
status is set on the hook, with the crew member the yard acts as. `hook add`
replaces the definition, so the source and the secret file are given again:

```sh
yardr hook add linear --source linear --secret-file ~/.yardr/linear.secret --as yardmaster \
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

The crew member must have an identity in the source (step 4), and nothing is
moved without `set_state` (step 5). The daemon makes the calls, so it must
run.

### 8. A test delivery

Before the service sends anything, a delivery signed by hand shows that the
listener, the hook, the secret file and the subscriptions hold. This is how
the adapter's tests sign theirs: the hex HMAC-SHA256 of the body with the
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
    16 10-06 11:00:27  -        hook           hook:linear  action=create actor={id=aacdca22-6266-4c0a-ab3c-8fa70a26765c name=Jane Doe} deliveries={yardmaster=all} delivery=52C4DEDF-79AF-41ED-82C4-B38CBB085A37 entity=539068e2-ae88-4d09-bd75-22eb4a59612f fields={label=[ui] team=[YRD Yardr]} hook=linear mentions=[] new_mentions=[] source=linear text=The bead page should list the issue it tracks. thread={id=539068e2-ae88-4d09-bd75-22eb4a59612f title=Web view shows links url=https://linear.app/acme/issue/YRD-1/web-view-shows-links} type=issue url=https://linear.app/acme/issue/YRD-1/web-view-shows-links
```

and the crew member's inbox has it as a line (`yardr inbox --crew yardmaster`):

```
    16 10-06 11:00  hook (all) linear issue create by Jane Doe on "Web view shows links": The bead page should list the issue it tracks.  https://linear.app/acme/issue/YRD-1/web-view-shows-links
```

The same delivery signed with another secret is answered 401 and shows as

```
    15 10-06 11:00:27  -        hook_rejected  hook:linear  count=1 hook=linear last=2026-10-06T09:00:27Z reason=unauthorized status=401
```

and in `yardr hook list` under LAST REFUSED (`10-06 11:00 unauthorized`),
beside LAST ACCEPTED. A hook with neither time has been sent nothing this
yard's server saw: then the delivery went elsewhere or was not sent, and
the service's own delivery log says which. `yardr hook show 16` prints the
raw body.

### 9. The yardmaster answers, and a merged bead closes the issue

*The crew member's own session, or the operator, for the answer; anyone for
the bead and the link.*

```sh
yardr bead create "Web view shows links" --depot work --tracks 16   # the issue of delivery 16
# work-h6ef
yardr link list work-h6ef
# tracks  https://linear.app/acme/issue/YRD-1 (linear)
yardr thread show 16                                      # (in Linear) the issue's comments
yardr reply 16 "Filed as work-h6ef; I will post here when it lands."   # (in Linear)
```

The last two call Linear with the credential of step 3, from the crew member's
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
assembly ([pipeline.md](pipeline.md)); the map hears the stage however the
bead got there. While a move waits or is retried, `yardr hook show linear`
lists it (`to do: work-h6ef (task in new) -> https://linear.app/acme/issue/YRD-1 started`).

### GitHub, where it differs

The same steps, with these differences:

- **The hook.** `yardr hook add gh --source github --secret-file
  ~/.yardr/github.secret`. A repository webhook is set per repository, with
  content type `application/json` and the secret from the file. The
  delivery is signed in `X-Hub-Signature-256: sha256=<hex HMAC-SHA256 of
  the body>` and names its event in `X-GitHub-Event`; no timestamp is
  signed, so a test delivery needs none.
- **A GitHub App** does both sides at once: its one webhook delivers every
  repository it is installed on, and the crew member answers as the App's bot.
  Its credential is two lines in the `env_file`, `GITHUB_APP_ID` and
  `GITHUB_APP_PRIVATE_KEY_FILE`, in place of `GITHUB_TOKEN`. yardr signs a
  short-lived token with the key for each repository it calls
  ([subscriptions.md](subscriptions.md#as-a-github-app),
  [crew.md](crew.md#as-a-github-app)).
- **The identity** is a key only: the login in lowercase, `yardr crew
  identity yardmaster --source github --key <login>`; for an App `--key
  '<slug>[bot]'`.
- **The states.** An issue is open or closed, so a map may say `started`
  and `done`; `review` is refused (`github: an issue has no review status:
  the states it has are started and done`). A closed issue is never
  reopened by a map, and a pull request is never touched.
- **Links** are to issues only: a pull request's URL is refused.
- **Where keys.** Besides the common ones: `event`, `number`, `state`,
  `merged`, `conclusion`, `workflow`, `head_branch`, `head_sha`, `head_repo`,
  `base`, `train`, `sender`.
- **Events move trains.** A github hook's events about a train's pull
  request (`train`: the head branch is `yard/<train>`) are put to the
  hook's table of what they say (`yardr hook add --says`, or a pack's
  `sources.github.says`), and the line an event matches reports an outcome
  for the train, which its flow routes. yardr holds no rule of its own for
  them ([links.md](links.md#the-sources-events-move-the-bead),
  [pipeline.md](pipeline.md#trains)).
- **A thread** is `<owner>/<repo>#<number>`, and `yardr thread open github
  --in <owner>/<repo>` names a repository where Linear names a team.

A signed GitHub comment that mentions the crew member's login, sent to the
isolated yard, was recorded as

```
    34 10-06 11:01:17  -        hook           hook:gh      action=created actor={id=jane name=Jane} deliveries={yardmaster=mentioned} … fields={action=[created] event=[issue_comment] label=[bug] number=[7] repo=[acme/webapp] state=[open]} hook=gh mentions=[yardmaster-bot] message=991 new_mentions=[yardmaster-bot] source=github text=@yardmaster-bot can you look? thread={id=acme/webapp#7 title=Retries drop the last attempt url=https://github.com/acme/webapp/issues/7} type=issue_comment …
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

What someone writes today to make yardr carry a new service. For a service
that only has to tell a person, it is a file and no Go: [a declared
source](#a-declared-source) with a `notify` key. For everything else it is
Go, in yardr's own repository.

**The least: receive.** A new file `internal/hooks/<source>.go` with a type
that has `Verify`, `Normalize` and `Bodies`, and one line in the `Adapters`
map in `hooks.go`. From that line on `yardr hook add --source <source>` and
`yardr subscribe <name> <source>` take the name, deliveries become events,
and subscriptions with `type` and `action` filters work. What `Normalize`
must get right for the rest to work: `actor.id`, `mentions`, `assignee` in
the form an identity will be given in; `thread.id` the same for every
delivery of one issue; `entity` wherever the service re-sends a text.

**Then, each as wanted:**

| to have | write | and also |
|---|---|---|
| `--where` on the source's own fields | `fields` in `Normalize` | the key in `WhereKeys` or a list of the source's own in `internal/store/subscriptions.go` (`whereKeys`), which is what `subscribe` accepts; the help of `yardr subscribe` names the keys by hand |
| `yardr link` | `IssueHost` and `Issue` (`issue.go` has both sources') | nothing: `LinkSources` finds it |
| `yardr reply`, `thread show` | `Credential`, `Reply`, `Posted`, `Thread` (`reply.go`) | the names of its credential in `credentialWays` (`reply.go`), which `crew show`, the pack plan and the daemon read |
| `yardr thread open` | `Place`, `Open`, `Opened` (`open.go`) | nothing |
| `yardr thread state` | `SetState` (`state.go`): what `started`, `review` and `done` mean there | nothing |
| a stage map | `IssueThread`, `CredentialFrom`, `Places`, `Forward` (`forward.go`) | nothing: `hook add --map`, the pack's `sources` and the daemon ask `ForwarderFor` |

Two more places know sources by name: `internal/standin`, the stand-in for
the services' APIs that the tests and an isolated yard use, and the docs.

**The tests the two adapters have**, in `internal/hooks` unless said:

- receive: `TestLinearSignature`, `TestLinearStaleTimestamp`,
  `TestLinearDuplicateDelivery`, `TestLinearNormalize`,
  `TestLinearPlainMentions`, `TestGitHubSignature`,
  `TestGitHubDuplicateDelivery`, `TestGitHubReplayUnderNewDeliveryID`,
  `TestGitHubNormalize`, `TestGitHubMentions`, `TestTokenSource`,
  `TestTokenDedupesOnDeliveryIDOnly`, over recorded payloads in
  `testdata/`;
- identify, through the store: `TestLinearMentionReachesInbox`,
  `TestLinearDelegateReachesInbox`, `TestGitHubMentionReachesInbox`,
  `TestIssueUpdateDeliversOnlyNewMentions`,
  `TestCommentEditDeliversOnlyNewMentions`,
  `TestGitHubCommentEditDeliversOnlyNewMentions`;
- link: `TestIssueFixedForm`, `TestIssueInItsSource`,
  `TestDeliveryThreadIsItsIssue`, `TestStoreLinksOneIssueUnderAnySpelling`;
- reply: `TestLinearReplyUnderRoot`, `TestLinearReplyKey`,
  `TestLinearThread`, `TestGitHubReplyAndThread`, `TestGitHubReplyKey`,
  `TestCredentials`, and the GitHub App's in `githubapp_test.go`;
- open and state: `TestLinearOpen`, `TestGitHubOpen`, `TestLinearSetState`,
  `TestGitHubSetState` and their refusals;
- move: `TestLinearForward`, `TestLinearForwardWithoutReviewStatus`,
  `TestGitHubForward`, `TestForwardRefusals`, `TestIssueThread`, and in
  `internal/dispatch` `TestIssueFollowsItsBead`,
  `TestIssueOnlyMovesForward`, `TestIssueNeedsSetState`;
- the commands, in `internal/cli`: `TestHookAddGitHub`, `TestHookAddMap`,
  `TestReply`, `TestThreadOpen`, `TestThreadStateLinear`,
  `TestThreadStateGitHub`, `TestLinkCommands`.

The outbound tests run against fake servers the test starts, reached
through `YARDR_LINEAR_API` and `YARDR_GITHUB_API`.

**What checks that an adapter conforms.** The compiler, for `Adapter`: a
type in the `Adapters` map that lacks one of the three methods does not
build. For the rest nothing does. A capability is found by a type
assertion at run time, so a method with a wrong signature is a source
without the capability, silently. What says which source has what are four
tests that name the sources: `TestRepliable`, `TestOpenable`,
`TestStateable` and the `Forwardable` check in `TestIssueThread` (each
expects `github, linear`), and `LinkSources` in `TestIssueInItsSource`. A new
source changes those lists by hand. There is no suite that takes any
adapter and checks its behaviour: each adapter has tests of its own, over
its own payloads.

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
3. **An adapter is Go code inside yardr.** A new service is a change to
   `internal/hooks`, the store's where keys and the tests, and a release.
   A declared source is the first thing outside the core, and it can only
   tell a user: receiving, replying and moving an issue cannot be declared.
   yardr-s48v holds the idea of adapters outside the core, and the
   direction is that Linear leaves the binary too.
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
   A declared source's is checked: `yardr source check`, `yardr doctor
   --try`, and doctor's note on a last call answered 401 or 403. That is
   not there for `linear` and `github`.
7. **The identity is looked up by hand.** yardr holds the token and could
   ask the service who it is; no command does, and a wrong id fails
   silently: the crew member is never mentioned and hears its own actions.
8. **No test delivery.** Checking a hook end to end takes a real event in
   the service or a body signed by hand, as in step 8.
9. **yardr does not know its public address** and makes no webhook in the
   service. The URL to paste is assembled by the person, and a webhook
   pointing at a yard that is gone stays in the service.
10. **Where keys are a list in the store, not the adapter's.** A field an
    adapter sets is filterable only when `internal/store/subscriptions.go`
    names it: `--where` on a field the list leaves out is refused. A test
    holds the two together for what the fixtures cover
    (`TestAdapterFieldsAreWhereKeys` in `internal/hooks`): it normalizes
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
    backlog) cannot be named by a map or by `yardr thread state`.
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
