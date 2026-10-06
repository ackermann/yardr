# Integrations

An integration is a service outside the yard that the yard hears and, where
the service allows it, speaks in: Linear, GitHub. This page says what one is
made of today, how one becomes part of a yard, with Linear as the example
from nothing to a merged bead closing its issue, and what someone would
write to add a service. It ends with what is missing.

It describes what is built. The other pages go deeper and are linked where
they do: [subscriptions.md](subscriptions.md), [links.md](links.md),
[residents.md](residents.md), [packs.md](packs.md),
[configuration.md](configuration.md).

## What an integration is

Two things, kept in two places:

- **An adapter**: Go code in `internal/hooks`, compiled into yardr. One per
  service, registered by name in the map `hooks.Adapters`. The name is the
  **source**: `linear`, `github`, `token`. yardr has these three and no way
  to load another.
- **Settings in the yard**: a hook (the endpoint and its secret), a
  resident's credential, identity and grants, subscriptions, links, a stage
  map. They live in the store and in files in the yard's home, and are set
  with the commands of [part 2](#how-one-becomes-part-of-a-yard).

The adapter is a set of capabilities. Each is a Go interface, and a source
has a capability exactly when its adapter implements the interface: the
code asks with a type assertion (`hooks.ReplierFor`, `OpenerFor`,
`StaterFor`, `ForwarderFor`, `LinkSources`) and refuses the command for a
source that lacks it, naming those that have it (`yardr cannot open threads
in token (it can in github, linear)`). Only the first is required.

| capability | interface (file) | `linear` | `github` | `token` |
|---|---|---|---|---|
| receive | `Adapter` (`hooks.go`) | yes | yes | yes |
| identify | none: read from the envelope by the store | mentions, assignee, delegate | mentions, assignee | never |
| link | `Linker` (`issue.go`) | yes | yes, issues only | no |
| reply, read a thread | `Replier` (`reply.go`) | yes | yes | no |
| open a thread | `Opener` (`open.go`) | yes | yes | no |
| set a thread's status | `Stater` (`state.go`) | `started`, `review`, `done` | `started`, `done` | no |
| move with a bead's stage | `Forwarder` (`forward.go`) | yes | yes | no |
| tell a user outside the yard | not built | no | no | no |

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
residents it reached, each with the reason), `notified` (the users) and
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

*What for.* To know which resident a delivery is about, and which
deliveries are the resident's own.

*No interface.* The adapter only fills `actor.id`, `mentions`, `assignee`
and `delegate`. The store compares them with the identity each resident has
in the source:

```sh
yardr resident identity yardmaster --source linear --id <user id> --key <profile key>
```

An identity is an `id`, a `key`, or both. An id matches exactly; a key is a
name people type and matches in any case. Linear's adapter reports a
mention as a user id when the payload carries the rich text, and as the
profile key (`@yardr`) when it carries markdown only, so a Linear identity
wants both. GitHub's reports logins in lowercase, so a GitHub identity is
`--key <login>`; a GitHub App's is `--key '<slug>[bot]'`, and `@<slug>`
mentions it.

With that, for each resident subscribed to the source:

- a delivery whose `actor.id` is the resident's identity never reaches it.
  It makes the resident a **participant** in the delivery's thread;
- **mentioned**: its identity is among the new mentions;
- **assigned**: it is the `assignee` or the `delegate`;
- **participant**: it takes part in the thread. A resident takes part in a
  thread once it acted there (above), answered there (`yardr reply`),
  opened it (`yardr thread open`) or follows it (`yardr thread follow`).
  `yardr thread list <resident>` shows them, each with how.

**Involved** is the subscription scope that puts these together:
`involved` delivers what mentions the resident, what is assigned or
delegated to it and what happens in a thread it takes part in. `mentions`
delivers the first only, `all` everything from the source
([subscriptions.md](subscriptions.md#scopes)). A resident without an
identity in the source is never mentioned or assigned, and hears its own
actions as someone else's.

A user ([users.md](users.md)) has no identity in a source: only `--scope
all` reaches a person.

*Where a user sees it.* `yardr resident identity`, `yardr subscribe`,
`yardr resident show` (identities and subscriptions), `yardr thread list`,
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

*What for.* A resident speaks in the service as itself: it answers a
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

*What the yard does with it.* The server never speaks and never holds a
credential. The CLI in the resident's pane makes the call, with what the
resident's `env_file` or `env_command` put in the pane's environment
([residents.md](residents.md#outbound-credentials)). Only the resident's
own session or the operator may speak as a resident. Replying needs no
grant. Opening a thread needs `open_threads=<source>` on the resident, and
setting a status needs `set_state=<source>` and a thread the resident takes
part in; only the operator may grant either. After each, the resident takes
part in the thread and an event lands on its journal (`replied`,
`thread_opened`, `thread_state`).

*Where a user sees it.* `yardr reply <seq>`, `yardr thread show <seq>`,
`yardr thread open <source> --in <place>`, `yardr thread state <seq>
<state>`, `yardr resident set <name> open_threads=… set_state=…`, `yardr
resident show` ([residents.md](residents.md#speaking-in-sources)).

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
- `CredentialFrom` is `Credential` for a caller that is not the resident's
  pane: it reads the names from what the resident's `env_file` sets.
- `Places` says whether the source has a status for a state at all, so a
  map that names one it lacks is refused when it is written (`review` on
  GitHub).
- `Forward` is `SetState` that only moves an issue further than it stands.

*What the yard does with it.* A hook carries a map from `<bead
type>:<stage>` to a state and the resident it acts as. `yardr hook list`
shows it in its `MOVES ISSUES` column. When a bead with a `tracks` link to
an issue of the hook's source enters a mapped stage, the move is written
with the bead's event and made by the daemon afterwards, with the
credential in that resident's `env_file`, and only when the resident has
`set_state` for the source. It is retried when the service fails, and ends
in a `thread_state` or `thread_state_skipped` event on the bead
([links.md](links.md#a-beads-stage-moves-the-issue)).

*Where a user sees it.* `yardr hook add --as <resident> --map
<type>:<stage>=<state>`, `yardr hook list`, `yardr hook show <name>` (the
map and the moves still to make), `yardr events --bead <id>`.

### What is not there

Telling a user outside the yard. A resident is nudged in its pane; a person
sees mail and notifications only when they look (`yardr inbox`, `yardr
chat`). No adapter has a method that carries a notification to a person in
the service, on the desktop or in a chat (*planned, yardr-1481*).

## How one becomes part of a yard

The steps as they are today, with Linear, for a resident called `yardmaster`.
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
One hook serves every resident.

*(in Linear)* Settings → API → Webhooks: the URL
`https://<public host>/hooks/linear`, the events Issues and Comments. Its
signing secret is what goes in the file.

Check: `yardr hook list` shows the hook with STATE `ok`, and `-` under LAST
ACCEPTED until a delivery came.

### 3. The resident's credential

*Anyone, for the file; the token is made in Linear.* What the resident
speaks with goes in an env file of its own, never in the store:

```sh
echo 'LINEAR_API_KEY=<token>' > ~/.yardr/residents/yardmaster.env
chmod 600 ~/.yardr/residents/yardmaster.env
yardr resident set yardmaster env_file=~/.yardr/residents/yardmaster.env
yardr resident show yardmaster
#   env_file …/residents/yardmaster.env (LINEAR_API_KEY)
#   linear credential: API key (LINEAR_API_KEY)
```

`env_command` names a command that prints the environment in place of a
file ([residents.md](residents.md#secrets-from-a-provider)). The file is
read when the resident starts: `yardr resident restart yardmaster` after a
change.

*(in Linear)* The token is that of a Linear OAuth application made for the
resident, so the resident acts as the application's own user and not as a
person ([subscriptions.md](subscriptions.md#setting-up-linear)).

Nothing checks the token against Linear at this step: `resident show`
reads the names in the file, not whether the service takes the value.

### 4. The resident's identity

*Anyone.* The id and the profile key of the user the token acts as:

```sh
yardr resident identity yardmaster --source linear --id <user id> --key <profile key>
# SOURCE  ID                                    KEY
# linear  11111111-2222-4333-8444-555555555555  yardr
```

Both are looked up in Linear by hand; yardr has no command that asks the
service who a token is.

### 5. The grants

*The operator only.* Answering needs none. Opening issues and setting
their status do, and the stage map of step 7 needs `set_state`:

```sh
yardr resident set yardmaster open_threads=linear set_state=linear
#   open_threads: (not set) -> linear
#   set_state: (not set) -> linear
```

From anyone else: `open_threads: only the operator (jo) may let yardmaster open
threads in linear; this is mckean`.

### 6. Subscriptions

*Anyone, for a resident.* What of the source reaches the resident's inbox:

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

*The operator, or the resident's own session.* Which stage means which
status is set on the hook, with the resident the yard acts as. `hook add`
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

The resident must have an identity in the source (step 4), and nothing is
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

and the resident's inbox has it as a line (`yardr inbox --resident yardmaster`):

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

*The resident's own session, or the operator, for the answer; anyone for
the bead and the link.*

```sh
yardr bead create "Web view shows links" --rig work --tracks 16   # the issue of delivery 16
# work-h6ef
yardr link list work-h6ef
# tracks  https://linear.app/acme/issue/YRD-1 (linear)
yardr thread show 16                                      # (in Linear) the issue's comments
yardr reply 16 "Filed as work-h6ef; I will post here when it lands."   # (in Linear)
```

The last two call Linear with the credential of step 3, from the resident's
pane; the operator runs `yardr reply` with `--resident yardmaster` and the
credential in the command's environment. From anyone else:
`reply --resident yardmaster: only yardmaster's own session or the operator (jo) may speak as yardmaster; this is mckean`.
The stand-in does not know comments, so the post itself was not made there.

The link is the issue's fixed form, without the title the delivery's URL
had. A bead created into a mapped stage moves the issue at once, and each
later stage does as the map says:

```sh
yardr bead advance work-h6ef --to merged
yardr events --bead work-h6ef
#   21 … work-h6ef thread_state  hook:linear  cause=18 hook=linear resident=yardmaster source=linear stage=new state=started status=In Progress … was=Todo
#   24 … work-h6ef thread_state  hook:linear  cause=23 hook=linear resident=yardmaster source=linear stage=merged state=done status=Done … was=In Progress
```

In a yard with a flow the bead reaches `merged` through review and the
refinery ([pipeline.md](pipeline.md)); the map hears the stage however the
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
  repository it is installed on, and the resident answers as the App's bot.
  Its credential is two lines in the `env_file`, `GITHUB_APP_ID` and
  `GITHUB_APP_PRIVATE_KEY_FILE`, in place of `GITHUB_TOKEN`. yardr signs a
  short-lived token with the key for each repository it calls
  ([subscriptions.md](subscriptions.md#as-a-github-app),
  [residents.md](residents.md#as-a-github-app)).
- **The identity** is a key only: the login in lowercase, `yardr resident
  identity yardmaster --source github --key <login>`; for an App `--key
  '<slug>[bot]'`.
- **The states.** An issue is open or closed, so a map may say `started`
  and `done`; `review` is refused (`github: an issue has no review status:
  the states it has are started and done`). A closed issue is never
  reopened by a map, and a pull request is never touched.
- **Links** are to issues only: a pull request's URL is refused.
- **Where keys.** Besides the common ones: `event`, `number`, `state`,
  `merged`, `conclusion`, `workflow`, `head_branch`, `head_sha`, `base`,
  `train`.
- **A thread** is `<owner>/<repo>#<number>`, and `yardr thread open github
  --in <owner>/<repo>` names a repository where Linear names a team.

A signed GitHub comment that mentions the resident's login, sent to the
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
| the credential's names | `yardr resident show <name>`: `<source> credential: …`. Whether the service takes it shows at the first `yardr thread show <seq>` |
| identity, grants, subscriptions | `yardr resident show <name>` |
| the stage map | `yardr hook list` (MOVES ISSUES), `yardr hook show <name>` |
| a pack's view of all of it | `yardr pack check` and `pack apply` print, per source a pack names, what the yard has and lacks: hook, secret file, identity, credential, listener ([packs.md](packs.md#how-a-pack-maps-to-the-yard)) |

## Adding a service

What someone writes today to make yardr carry a new service. All of it is
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
| `yardr reply`, `thread show` | `Credential`, `Reply`, `Posted`, `Thread` (`reply.go`) | the names of its credential in `credentialWays` (`reply.go`), which `resident show`, the pack plan and the daemon read |
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
   resident show` each show a part.
2. **A pack carries part of it, and only part.** From `pack.yaml`: a
   resident's unfiltered subscription to a source
   (`residents.<name>.subscribe.<source>: <scope>`), the request that the
   resident may open threads there (`open_threads`, which the import puts
   to the operator), and the stage map with the resident it acts as
   (`sources.<source>`). A pack does not make the hook, the secret file or
   `hook_addr`, does not set the credential or the identity, cannot ask for
   `set_state`, and carries no subscription with a `--where`. Its plan is
   the nearest thing to a check of a whole integration, and it exists only
   for a source a pack names.
3. **An adapter is Go code inside yardr.** A new service is a change to
   `internal/hooks`, the store's where keys and the tests, and a release.
   yardr-s48v holds the idea of adapters outside the core.
4. **A user outside the yard is not told** (yardr-1481): no capability
   carries a notification to a person.
5. **No command says what a source can do.** The capability table above is
   read from the code. A user learns that `token` cannot be answered in
   from the refusal.
6. **Nothing checks a credential until it is used.** `yardr resident show`
   and the pack plan read the names in the `env_file`. A wrong or expired
   token shows at the first reply, and for a stage map as a move that
   fails and is retried (`yardr hook show <name>` has the last error).
7. **The identity is looked up by hand.** yardr holds the token and could
   ask the service who it is; no command does, and a wrong id fails
   silently: the resident is never mentioned and hears its own actions.
8. **No test delivery.** Checking a hook end to end takes a real event in
   the service or a body signed by hand, as in step 8.
9. **yardr does not know its public address** and makes no webhook in the
   service. The URL to paste is assembled by the person, and a webhook
   pointing at a yard that is gone stays in the service.
10. **Where keys are a list in the store, not the adapter's.** A field an
    adapter sets is filterable only when `internal/store/subscriptions.go`
    names it. GitHub's `head_repo` is set by the adapter and is not in the
    list, so `--where head_repo=…` is refused.
11. **Conformance is not checked** beyond the three methods of `Adapter`
    (above).
12. **A reply needs a delivery.** `yardr reply` answers a hook event by its
    seq. A resident cannot comment on an issue that sent the yard nothing,
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
    its status, for both services. A reply and an opened thread cannot be
    tried in an isolated yard: they are covered by the tests' fake servers
    and by the real service only.
