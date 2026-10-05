# Subscriptions and notifications

A resident hears of things through its inbox (`yardr inbox`). Mail to it
always lands there. Everything else arrives because something subscribed it.
A person subscribes the same way, and what matches is a notification in the
user's inbox, kept apart from the mail ([below](#users-as-subscribers)):

| source | what it delivers | how a resident subscribes |
|---|---|---|
| a hook source (`github`, `linear`, `token`) | webhook deliveries taken by `yardr hook` | `yardr subscribe <name> linear …` |
| `yardr` | yardr's own bead events: created, advanced, noted, closed, … | `yardr subscribe <name> yardr …`, `yardr bead watch` |
| `--on` on the resident | event kinds, every bead, and events not about a bead | `yardr resident add --on …` |

Every subscription has the same shape: a **source**, a **scope** (`all`,
`mentions`, `involved`) and **where** filters (exact matches, all of which
must hold). Matching happens once, when the event is logged, against the
subscriptions as they are then. The event records who it reached and why
(`deliveries`), and `yardr inbox` shows that reason next to each event. A
resident never hears of its own actions, nor of what happens on its own
journal bead (its session starting and stopping, its journal entries and
handoffs, its subscriptions changing), whoever logged it; a plain note
someone else leaves on its journal still reaches it.

```sh
yardr subscribe mayor yardr --done                         # every bead that becomes done
yardr subscribe mayor yardr --done --where rig=yardr --where label=ui
yardr subscribe mayor yardr --scope involved               # beads it created, advanced or noted, and events @mentioning it
yardr bead watch yardr-k3f9 --resident mayor --done         # this one bead, until it closes
yardr subscribe mayor linear --scope involved              # Linear issues it is mentioned in, assigned, or works on
yardr resident show mayor                                  # lists its subscriptions
yardr unsubscribe mayor yardr [--scope …] [--where …]
```

The source can be the second argument or `--source`.

## Scopes

| scope | hook source | yardr |
|---|---|---|
| `all` | every delivery | every bead event |
| `mentions` | deliveries that mention its identity | `@<name>` in the event itself (see below) |
| `involved` | mentions, plus deliveries on what is assigned or delegated to it, plus threads it participates in | mentions, plus events on beads it created, advanced or noted |

The default is `involved` for a hook source and `all` for yardr, where the
where filters usually do the narrowing.

Reasons, from most to least specific: `done` (a done subscription matched),
`mentioned`, `assigned`, `participant`, `all`. When several subscriptions
match, the most specific reason is recorded.

A Linear issue has an assignee and a delegate: the delegate is the agent
the issue is handed to, while the assignee stays the person who handed it
over. A resident is usually the delegate, so an issue whose delegate is its
identity reaches it under `involved` as one assigned to it does, with
reason `assigned`: when it is created that way, and when the delegate is
set later. The event carries the id as `delegate`, beside `assignee`.

## The yardr source

Bead events from the event log.

**Mentions count in the event that makes them.** `@<name>` is looked for in
what the event itself says: the note it added (`noted`, `journal`,
`handoff`), the body of the bead it created, and its own fields (a close or
hold reason). A note that said `@b` once does not make every later event on
that bead mention `b`, so two residents mentioning each other in their
journals nudge each other once per mention, not forever. A `state=done`
subscription is the exception: it hears of a bead once, so for it a mention
anywhere on the bead (body or any note) counts.

**Journals and memories are out of the source** unless a subscription names
them with `bead=` or `type=` (`--where type=journal`, `--where
bead=mayor-journal`). A resident subscribed to `all` does not hear every
other resident's journal entries; a mention of it in one still reaches it.

Where keys:

| key | matches |
|---|---|
| `bead` | the bead (stored as its handle; a UUID works too) |
| `rig`, `type` | the bead's rig and type |
| `label` | any of the bead's labels |
| `kind` | the event: `created`, `advanced`, `noted`, `held`, `unheld`, `released`, `claimed`, `closed`, `reopened`, `gave_up`, `session_blocked`, `session_stalled`, `session_model_mismatch`, … |
| `stage` | the stage entered: `advanced` events (its `to`) and `created` (the first stage). Other events have no stage, so `stage=…` delivers moves into the stage and nothing else |
| `from` | the stage left (`advanced`) |
| `group` | the group that owns the bead, or for `advanced` and `closed`, the group whose session the move ended |
| `actor` | who acted |
| `epic` | the bead itself, or a bead with a direct `blocks` or `parent` edge to it (`yardr dep add <epic> <child> -k parent`). Edges are not followed further |
| `state` | `done`, see below; `--done` is short for `--where state=done` |

### Done

`state=done` matches a bead becoming done: its **first close**. Entering a
terminal stage of its flow closes the bead in the same write (with the stage
as reason), so this covers both a flow's terminal stage and a direct close
such as the refinery's `merged <sha>`. A done subscription hears of each
bead once: the refinery closing a bead that a terminal stage already closed
is an ordinary `closed` event, and so is a second close after `yardr bead
reopen`.

### Watching a bead

```sh
yardr bead watch <id> [--resident <name>] [--stage s] [--kind k] [--done]
yardr bead unwatch <id> [--resident <name>]
```

`watch` is sugar for `yardr subscribe <name> yardr --where bead=<id>`
(plus `stage`, `kind`, `state=done`). `--resident` defaults to
`$YARDR_RESIDENT`, so a resident's own session can watch without naming
itself. When the bead closes, the close is delivered first, then every
subscription with `bead=<id>` ends (the `closed` event lists whose in
`watches_ended`). A reopened bead is not watched any more. Watching (any
subscription with `bead=`) a bead that is already closed is refused: its
watch would have ended already.

## Groups as subscribers

A manual group can subscribe like a resident (`yardr subscribe approvers
yardr --done`). Its subscriptions deliver to each of its **members that is
a resident**, with the reason `<reason> via <group>` (e.g. `done via
approvers`). Scopes are worked out per member: `mentions` means that member
is @mentioned. Redefining the group keeps its subscriptions. A runner other
than manual cannot subscribe.

A group's subscriptions reach no person: a member who is a user
([users.md](users.md)) is told of what they subscribed to themselves
([below](#users-as-subscribers)), not of what their group did. Human members
are told in herdr when a bead reaches their manual group's stage (group
config `notify=true`).

## Users as subscribers

A user ([users.md](users.md)) subscribes as a resident does, with the same
sources, scopes and where keys:

```sh
yardr subscribe ada yardr --done --where rig=yardr         # every bead of the rig that becomes done
yardr subscribe ada yardr --where stage=decide             # every bead that enters decide
yardr subscribe ada yardr --where kind=gave_up             # every session that gave up
yardr subscribe ada github --scope all --where conclusion=failure
yardr user show ada                                        # lists them
yardr unsubscribe ada yardr [--scope …] [--where …]
```

**Who may.** The user themselves and the operator. Nobody else, and no
resident: a resident deciding what a person is told is what notifications
are there to end, and the refusal says so:

```
mayor is a resident, and a resident does not decide what a person is told: ada subscribes themselves, or the operator (mckean) does it for them
```

The check is the store's, on the command's actor
([users.md](users.md#who-is-acting)), so it holds for every client.

**A matching event is a notification**, not mail: a record of its own in the
user's inbox, with no sender and so nobody to answer. yardr writes it as the
event is logged. No model is called and no resident is nudged or spends a
turn on it. Its text is one line, made from the event in the words `yardr
inbox` shows a resident for the same delivery, with the bead's title after
it where there is a bead:

```
yardr-k3dy closed (done) by yardr-refinery reason=merged 1a2b3c4 on "Chat: a row for the yard"
hook (all) github workflow_run completed by jane on "Fix the gate": ci failure on main  https://github.com/…
```

The first is `<bead> <kind> (<reason>) by <actor> <key=value …>`, the
reason being why it reached the user ([Scopes](#scopes)). A closed bead's
line carries the commit when the event has one: its own `commit`, or the
one its reason names, as the refinery's `merged <sha>` does. The second is a
hook delivery: source, type and action, who acted, the thread's title, the
text and the link. The event's part is cut at 300 characters and the title
at 120, with `…`; the link is never cut.

What differs from a resident:

- A user is not told of what they did themselves, as a resident is not.
- In a hook source a user has no identity and takes part in no thread, so
  only `--scope all` reaches them there. Say it: a hook source's default
  scope is `involved`, and a user's `mentions` or `involved` subscription
  to one is stored and matches nothing. In the yardr source every scope
  works: `@ada` in a note mentions the user `ada`, and `involved` adds the
  beads they created, advanced or noted.
- There is no `--on` for a user, so yardr's events that are not about a
  bead reach no person this way.
- A subscription with `bead=<id>` ends when the bead closes, after the
  close is told, as a resident's watch does.

The event records whom it notified as `notified` (user to reason), beside
`deliveries` for the residents, so a view that follows the stream knows to
read the notifications again.

**The bound.** A user's inbox keeps the newest 500 notifications. Nobody
acknowledges a stream, so when a 501st arrives the oldest is dropped, read
or not. Mail has no such bound. Removing a user removes their subscriptions
and notifications, and a later user of the same name starts with neither.

Reading and acknowledging them is in
[users.md](users.md#the-inbox), and [`yardr chat`](chat.md#the-yard) shows
them in a row of their own.

Telling a person outside the yard (a desktop notification, a chat service)
is not built (*planned, yardr-1481*): a notification is what such a channel
would carry.

## `--on`

A resident's `--on` kinds (default: `gave_up`, `session_blocked`,
`session_stalled`, `session_model_mismatch`, `unrouted`, …) keep working, as **implicit `yardr all kind=<k>`
subscriptions**. They are not migrated into stored subscriptions. They are
checked when the inbox is read, not when the event is logged, so changing
`--on` also changes which unread events are in the inbox. Unlike the yardr
source they also cover events that are not about a bead. `yardr inbox` shows
the reason `on` for them.

Two of them are about a session's model ([groups.md](groups.md#models)).
`session_stalled`: a session whose agent sat idle after its first prompt
and did nothing, most often because the agent refused its model. Its `tail`
is the end of the agent's pane, so the inbox shows the agent's own error,
and the group starts no further session with that model meanwhile.
`session_model_mismatch`: a session whose agent reports another model
(`reported`) than it was asked for (`asked`). Both are bead events, so a
bead subscription hears them too (`yardr subscribe <name> yardr --where
kind=session_stalled`).

`session_prompt_gave_up`: a session whose agent never took its first prompt
([groups.md](groups.md#herdr-setup)). Its `hint` is the command that
gives the agent the prompt by hand, or, with `typed: true`, the Enter that
submits the prompt sitting in its input. A resident added before this kind joined
the default does not hear it until its `--on` names it.

To move a resident to subscriptions, add `yardr subscribe <name> yardr
--where kind=<k>` for each kind and drop those kinds from its `--on`.

## Hook sources

A webhook delivery (`yardr hook add`) becomes one `hook` event with a
normalized envelope. Who it reaches depends on the resident's identity in the
source:

```sh
yardr resident identity mayor --source linear --id <user id> --key <profile key>
yardr subscribe mayor linear --scope involved
yardr subscribe triage linear --scope all --where type=issue --where action=create --where team=ENG
yardr thread follow mayor --source linear <issue id>    # participate without having acted
yardr thread list mayor
```

Where keys: `type`, `action` (the envelope), and `team`, `project`, `repo`,
`label` (fields the adapter exposes; any value may match). Once a resident
acts in a thread (a Linear issue), answers there with `yardr reply`, or
opens it with `yardr thread open`
([residents.md](residents.md#opening-a-thread)), it participates there, and
`involved` also delivers what others do in it.
In a thread it takes part in it can also set the issue's status, when its
definition allows it: `yardr thread state`
([residents.md](residents.md#setting-a-threads-status)).

A hook also works the other way. With `--as <resident>` and `--map
<type>:<stage>=<state>` it hears the yard's own bead events and moves the
issue a bead tracks when the bead enters a stage, as that resident, with no
agent in between ([links.md](links.md#a-beads-stage-moves-the-issue)).
`yardr hook list` shows the map, and `yardr hook show <name>` the whole
definition.

An issue's description is re-sent whole with every update, so a mention there
counts once: yardr keeps who each issue's description mentioned and treats
only newly added names as mentions. A status or label change on an issue that
still mentions `@mayor` does not reach `mayor` as `mentioned` again (it can
still reach it as `assigned`, `participant` or `all`). The event keeps the
full list in `mentions`; `new_mentions` is what routing used. A comment is
the same: its creation counts every mention in it, an edit (Linear `update`,
GitHub `edited`) only the names it adds, and a deletion (`remove`,
`deleted`) tells no one and forgets the comment. The envelope's `entity`
names what is diffed: the issue or pull request, the comment, or a review.

### Setting up Linear

What runs today, from nothing to a resident answering in Linear:

1. **The resident's own Linear user.** Create a Linear OAuth application for
   the resident and take its developer token: the token acts as the app's
   own user (the mayor's is called Yardr). A personal API key works too, but
   then the resident speaks as that person.
2. **The token in its env_file**, never in the store:
   `LINEAR_API_KEY=…` in `~/.yardr/residents/<name>.env`, `chmod
   600`, `yardr resident add <name> --set env_file=…` (see
   [residents.md](residents.md#outbound-credentials)). Restart the resident
   after changing it.
3. **Its identity**: the app user's id and profile key, so mentions,
   assignments, delegations and its own actions are recognized:
   `yardr resident identity <name> --source linear --id <user id> --key <profile key>`.
4. **A subscription**: `yardr subscribe <name> linear --scope involved`.
5. **The hook**: give the yard an address to take webhooks on,
   `yardr settings set hook_addr 127.0.0.1:8790` (the server takes it at its
   next start: `yardr server stop`, and the next command starts it; `yardr
   doctor` says what it listens on), and
   `yardr hook add linear --source linear --secret-file ~/.yardr/linear.secret`.
   One hook serves every resident.
6. **The webhook**: in Linear, Settings → API → Webhooks, URL
   `https://<public host>/hooks/linear`, events Issues and Comments; put its
   signing secret in the secret file.
7. **The funnel**: something public in front of the hook listener, such as
   a tunnel or a reverse proxy of your own, which serves `https://<public
   host>` to the listener and nothing else.

A comment mentioning the resident then lands in its inbox (`mentioned`), and
it answers with `yardr reply <seq>` and reads the thread with `yardr thread
show <seq>` ([residents.md](residents.md#speaking-in-sources)). GitHub is
the same with a `GITHUB_TOKEN` or a [GitHub App](#as-a-github-app), its
login as identity and the webhook below.

### GitHub

```sh
yardr hook add gh --source github --secret-file ~/.yardr/github.secret
yardr resident identity mayor --source github --key mayor   # its login, lowercase
yardr subscribe mayor github --scope involved
yardr subscribe mayor github --scope all --where epic=yardr-sv6b --where conclusion=failure
```

On GitHub, in the repository's **Settings → Webhooks → Add webhook**:

- **Payload URL:** `https://<your host>/hooks/gh`, the hook listener
  (the `hook_addr` setting) behind whatever exposes it.
- **Content type:** `application/json`. The form-encoded type is refused.
- **Secret:** the contents of the `--secret-file`. Each delivery is checked
  against `X-Hub-Signature-256` (HMAC-SHA256 of the body).
- **Events:** *Let me select individual events*, then tick Pull requests,
  Pull request reviews, Issue comments, Issues, Workflow runs and Check
  suites. Other events are recorded too, but with only `event`, `action` and
  `repo` and no thread.

#### As a GitHub App

A GitHub App gives the yard both sides at once: its webhook delivers every
repository it is installed on, in place of a webhook per repository, and a
resident answers as the App's bot, `<slug>[bot]`, not under a person's
account. Create one private App per yard, owned by the yard's operator and
installed only on their account. Its single webhook URL and private key mean
a shared public App would send every installer's events to one machine. App
names are global; if the name you want is taken, try `<something>-yardr` or
`yardr-<owner>`. Create it under your account's **Settings → Developer settings →
GitHub Apps → New GitHub App**:

- **Webhook:** active; the URL is the hook's (`https://<your host>/hooks/gh`)
  and the secret the contents of the hook's `--secret-file`. An App's
  deliveries are signed and read as a repository webhook's are.
- **Repository permissions:** Pull requests *read and write*, Issues *read
  and write*, Contents *read*, Metadata *read*, Actions *read*, Checks
  *read*. Nothing else: the App comments, opens issues and sets their
  status, and does not push or merge.
- **Subscribe to events:** Pull request, Pull request review, Pull request
  review comment, Issue comment, Issues, Workflow run, Check suite. (A
  review comment is recorded with only `event`, `action` and `repo` for
  now, like any event yardr does not read further.)
- **Where can this GitHub App be installed:** *Only on this account*.

Then generate the App's private key, install the App on the repositories,
and give the resident the key and its identity:

```sh
# in the resident's env_file, in place of GITHUB_TOKEN
GITHUB_APP_ID=1234567            # the App ID, or its Client ID
GITHUB_APP_PRIVATE_KEY_FILE=~/.yardr/residents/yardr-app.pem   # mode 0600

yardr resident identity mayor --source github --key 'yardr[bot]'   # <slug>[bot], lowercase
```

[residents.md](residents.md#as-a-github-app) has the steps and what yardr
does with the key. Remove the webhooks of the repositories the App is
installed on once its own delivers: an App's delivery of an event is another
body than the repository webhook's, so the hook would record the event
twice.

GitHub has no mention of an App. `@yardr` in a comment notifies no one, and
links to the user named yardr if there is one; people write it anyway, as
they do for other Apps, and the App finds it in the comment's body. So does
yardr: a resident whose github key is `yardr[bot]` is mentioned by `@yardr`
(and by `@yardr[bot]`). What the bot itself does has `yardr[bot]` as its
`actor.id` and stays out of the resident's inbox; the user named yardr is
someone else.

The envelope's `type` is the GitHub event and `action` the body's action:

| event | actions | thread | text, mentions |
|---|---|---|---|
| `pull_request` | `opened`, `synchronize`, `reopened`, `ready_for_review`, `closed` (`merged` true or false), … | the pull request | its description |
| `pull_request_review` | `submitted` (`state` `approved`, `changes_requested`, `commented`) | the pull request | the review's body |
| `issue_comment` | `created`, `edited`, `deleted` (on a pull request or an issue) | the pull request or issue | the comment |
| `issues` | `opened`, `closed`, `edited`, … | the issue | its description |
| `workflow_run`, `check_suite` | `completed` (`conclusion` `success`, `failure`, `cancelled`, …) | the pull request GitHub lists for the run, if any | `<workflow> <conclusion> on <branch>` |

A thread is `<owner>/<repo>#<number>`; so is the entity whose mentions are
diffed for a description, while a comment's is `issuecomment-<id>` and a
review's `pullrequestreview-<id>`, as GitHub's own URL fragments name them.
Logins are lowercased in `actor.id`, `assignee`
and `mentions`, so a resident's github identity is its login in lowercase.
`@org/team` mentions, email addresses and mentions in code are not mentions.
Where keys, besides `type`, `action`, `repo` and `label`: `event`, `number`,
`state` (the issue's or pull request's, or the review's), `merged`,
`conclusion`, `workflow` (a run's workflow, or a check suite's app),
`head_branch`, `head_sha`, `head_repo` (owner/name of the repository the
head branch is in: a fork's for a fork's pull request or workflow run),
`base`, and `epic`: the yardr epic
when the head branch is `yard/<epic>`, so `--where epic=yardr-sv6b` follows an
epic's pull request and CI without parsing branch names.

### Redeliveries

A delivery is recorded at most once, also across server restarts and when
copies arrive at the same time: the store checks and records its keys in the
write that records the delivery. The keys, checked only after the delivery is
verified:

- **Linear:** the sha256 of the raw body. Every body carries the
  `webhookTimestamp` it was signed with, so a body seen before is the same
  delivery again, whatever its `Linear-Delivery` header says. A repeated
  `Linear-Delivery` id is dropped as well.
- **GitHub:** the sha256 of the raw body, per hook, and the
  `X-GitHub-Delivery` header, per hook. The signature covers the body alone,
  so a captured delivery sent again under a new id is still the body seen
  before, and is dropped. The key is per hook because GitHub sends one
  event's body to every webhook on the repository.
- **token:** the `Yard-Delivery` header, per hook, when the sender sets one.
  The body is not a key: a cron job may send `backup ok` every hour.

A duplicate is answered 200 with `{"duplicate": true, "seq": <first>}`, so the
sender stops retrying, and leaves no event.

### The secret file

A hook's `--secret-file` is held to the rule an `env_file` is
([residents.md](residents.md#outbound-credentials)): a regular file of mode
0600 or stricter. `yardr hook add` refuses one that is not, saves nothing and
says `chmod 600`.

The receiver checks again on every delivery, since the file can be loosened
or replaced after the hook was added. While the file fails the rule, or is
missing, the hook is **blocked**: every delivery is answered 503 and is not
verified against the file, so the sender retries as its own policy has it.
A blocked hook shows in three places:

- `yardr hook list`: its STATE column reads `blocked: <reason>` instead of
  `ok`, e.g. `blocked: secret file /home/me/.yardr/linear.secret is open to
  others (mode 0644); chmod 600 it`. The state is what the last delivery
  found, so a file broken since then shows on the next one.
- one `hook_blocked` event (`hook`, `path`, `reason`; never what the file
  holds) when the block begins, not one per refused delivery.
- the server log: `hook delivery rejected … reason=blocked status=503` for
  every refused delivery, and `hook unblocked` when it ends.

Fix the file and the next delivery clears the state; so does `yardr hook add`
of the same hook again. A file that passes the rule but is empty is not a
block: its deliveries are answered 500, as before.

Only the server's webhook listener records deliveries. The store socket has no
method to record one, nor to log an event of an arbitrary kind: clients log
dispatch decisions (`gave_up`, `session_blocked`, …) through a method that
accepts those kinds only.

## Planned

- `outcome` as a where key, once beads carry outcomes (*planned, yard-qexg*).
