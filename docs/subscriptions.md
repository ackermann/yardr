# Subscriptions and notifications

A crew member hears of things through its inbox (`yardr inbox`). Mail to it
always lands there. Everything else arrives because something subscribed it.
A person subscribes the same way, and what matches is a notification in the
user's inbox, kept apart from the mail ([below](#users-as-subscribers)):

| source | what it delivers | how a crew member subscribes |
|---|---|---|
| a hook source (`github`, `linear`, `token`) | webhook deliveries taken by `yardr hook` | `yardr subscribe <name> linear …` |
| `yardr` | yardr's own bead events: created, advanced, noted, closed, … | `yardr subscribe <name> yardr …`, `yardr bead watch` |
| `--on` on the crew member | event kinds, or `attention`, the class of kinds that need someone (the default); every bead, and events not about a bead | `yardr crew add --on …`, `yardr crew set <name> --on …` |

Every subscription has the same shape: a **source**, a **scope** (`all`,
`mentions`, `involved`) and **where** filters (exact matches, all of which
must hold; a key given more than once holds several values, any of which
matches: [below](#several-values-for-one-key)). Matching happens once,
when the event is logged, against the subscriptions as they are then. The
event records who it reached and why (`deliveries`), and `yardr inbox`
shows that reason next to each event. A
crew member never hears of its own actions, nor of what happens on its own
journal bead (its session starting and stopping, its journal entries and
handoffs, its subscriptions changing), whoever logged it; a plain note
someone else leaves on its journal still reaches it.

```sh
yardr subscribe yardmaster yardr --done                         # every bead that becomes done
yardr subscribe yardmaster yardr --done --where depot=yardr --where label=ui
yardr subscribe clerk yardr --where depot=web --where kind=held --where kind=gave_up   # either kind, in that depot
yardr subscribe yardmaster yardr --scope involved               # beads it created, advanced or noted, and events @mentioning it
yardr bead watch yardr-k3f9 --crew yardmaster --done         # this one bead, until it closes
yardr subscribe yardmaster linear --scope involved              # Linear issues it is mentioned in, assigned, or works on
yardr crew show yardmaster                                  # lists its subscriptions
yardr subscribe                                             # who is told what, the whole yard
yardr unsubscribe yardmaster yardr [--scope …] [--where …]
```

The source can be the second argument or `--source`.

### Several values for one key

`--where` filters are exact matches and all of them must hold. A key given
more than once holds each of its values, and any one of them matches; the
keys are still ANDed. So one subscription says "these kinds, in this
depot":

```sh
yardr subscribe clerk yardr --where depot=web --where kind=session_blocked --where kind=gave_up --where kind=held
yardr subscribe yardmaster yardr --done --where depot=yardr --where depot=web
yardr subscribe yardmaster github --scope all --where event=pull_request --where event=workflow_run
```

Where the kinds wanted are the ones that need someone, `kind=attention`
names them all, and one value is the filter: `yardr subscribe clerk yardr
--where depot=web --where kind=attention` is what needs someone in the depot
`web` ([the class](#the-class-attention)).

The key is repeated, and not a list in one value, because a value is then
everything after the `=`: a label `ui,api`, a workflow `build | test`, or a
value containing a line break is one value, as it always was, and nothing
needs escaping.

`yardr subscribe` with no arguments, `yardr crew show` and the
`subscribed` event print such a subscription as one row, the key once for
each value, as it is typed: `yardr all depot=web kind=gave_up kind=held
kind=session_blocked`. A line break within one value is printed as `\n` or
`\r` to keep the row on one line. The values are kept sorted and each once, so the
same ones in another order are the same subscription, to `subscribe` (which
keeps it once) and to `unsubscribe --where` (which wants exactly the
filters, every value of a key). In `--json` the key's string holds its
values separated by NUL (`"kind": "gave_up\u0000held"`); NUL cannot be
passed in a command argument, so a value that could be stored before this
change remains one value. A subscription with one value for each key is
stored and printed as before.

`bead` takes one value: a subscription to a bead ends when that bead
closes ([watching a bead](#watching-a-bead)), so each bead has its own.
`state` has one value to give, `done`.

Not there: narrowing a crew member's `--on` (its kinds stay yard-wide; a
subscription like the first above, or `depot=<d> kind=attention`, is how a
depot's kinds are heard, and bead events only, see [`--on`](#--on));
negation (`kind` is not `noted`); `yardr subscribe` telling that one row
covers another (a row for `kind=held` and one for `kind=held kind=gave_up`
are two rows, and a default `--on` kind counts as reaching someone only
through a row that filters on kinds alone); and any permission by depot: a
filter decides what is delivered, not what a crew member may do.

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
over. A crew member is usually the delegate, so an issue whose delegate is its
identity reaches it under `involved` as one assigned to it does, with
reason `assigned`: when it is created that way, and when the delegate is
set later. The event carries the id as `delegate`, beside `assignee`.

## The yardr source

Bead events from the event log.

**Mentions count in the event that makes them.** `@<name>` is looked for in
what the event itself says: the note it added (`noted`, `journal`,
`handoff`), the body of the bead it created, and its own fields (a close or
hold reason). A note that said `@b` once does not make every later event on
that bead mention `b`, so two crew members mentioning each other in their
journals nudge each other once per mention, not forever. A `state=done`
subscription is the exception: it hears of a bead once, so for it a mention
anywhere on the bead (body or any note) counts.

**Journals and memories are out of the source** unless a subscription names
them with `bead=` or `type=` (`--where type=journal`, `--where
bead=yardmaster-journal`). A crew member subscribed to `all` does not hear every
other crew member's journal entries; a mention of it in one still reaches it.

Where keys:

| key | matches |
|---|---|
| `bead` | the bead (stored as its handle; a UUID works too) |
| `depot`, `type` | the bead's depot and type |
| `label` | any of the bead's labels |
| `kind` | the event: `created`, `advanced`, `noted`, `held`, `unheld`, `released`, `claimed`, `closed`, `reopened`, `gave_up`, `session_blocked`, `session_stalled`, `session_model_mismatch`, … |
| `stage` | the stage entered: `advanced` events (its `to`) and `created` (the first stage). Other events have no stage, so `stage=…` delivers moves into the stage and nothing else |
| `from` | the stage left (`advanced`) |
| `group` | the group that owns the bead, or for `advanced` and `closed`, the group whose session the move ended |
| `actor` | who acted |
| `train` | the bead itself, or a bead with a direct `blocks` or `parent` edge to it (`yardr dep add <train> <child> -k parent`). Edges are not followed further |
| `state` | `done`, see below; `--done` is short for `--where state=done` |

### Done

`state=done` matches a bead becoming done: its **first close**. Entering a
terminal stage of its flow closes the bead in the same write (with the stage
as reason), so this covers both a flow's terminal stage and a direct close
such as the assembly's `merged <sha>`. A done subscription hears of each
bead once: the assembly closing a bead that a terminal stage already closed
is an ordinary `closed` event, and so is a second close after `yardr bead
reopen`.

### Watching a bead

```sh
yardr bead watch <id> [--crew <name>] [--stage s] [--kind k] [--done]
yardr bead unwatch <id> [--crew <name>]
```

`watch` is sugar for `yardr subscribe <name> yardr --where bead=<id>`
(plus `stage`, `kind`, `state=done`). `--crew` defaults to
`$YARDR_CREW`, so a crew member's own session can watch without naming
itself. When the bead closes, the close is delivered first, then every
subscription with `bead=<id>` ends (the `closed` event lists whose in
`watches_ended`). A reopened bead is not watched any more. Watching (any
subscription with `bead=`) a bead that is already closed is refused: its
watch would have ended already.

## Who is told what

`yardr subscribe` without arguments changes nothing and prints every
subscriber side by side: a row for each thing subscribed to, a column for
each crew member, each user and each manual group that subscribes. A `--on`
kind is the row of the subscription it works as, `yardr all kind=<k>`. The
class is one row, `yardr all kind=attention`, and not a row for each of its
kinds: a yard whose crew were added without `--on` has that row and no other
for `--on`.

```
SUBSCRIPTION                 REACHES  clerk          yardmaster  jo (user)  approvers (group)
yardr all kind=gave_up       1        -              on          -          -
yardr all kind=held          2        on             on+sub      -          -
linear involved              1        -              sub         -          -
yardr all stage=approved     1        via approvers  -           -          sub
yardr all state=done         3        via approvers  sub         sub        sub
approvers (group) reaches: clerk
reaches no one (kinds of attention, a new crew member's --on, that nobody has): session_blocked, unrouted, …
```

- `on`: the kind is in the crew member's `--on`. `sub`: a subscription of
  its own. `on+sub`: both, so the kind arrives by two paths and one of them
  can go (an event still arrives once).
- `via <group>`: the crew member is a member of a manual group that
  subscribes. The group's own column reads `sub`, and the line under the
  matrix names the crew members it reaches.
- `REACHES` counts the inboxes the row arrives in: crew members and users,
  not groups. 2 or more is an overlap; 0 is a group's subscription with no
  crew member among its members.
- `reaches no one` lists the kinds of the class `attention`, a new crew
  member's default `--on`, that are in no crew member's `--on` and in no
  `yardr all kind=<k>` subscription that reaches someone: `--on` replaces
  the list, so a crew member given a few kinds lost the rest
  ([below](#--on)). A row that names the class, in `--on` or as a
  subscription with no other filter, counts for every kind of it, so the
  line is not printed while someone hears the class. A kind someone has
  only as a subscription is not listed, though the subscription does not
  carry its events that are about no bead.

Rows are told apart by their exact source, scope and filters: `yardr all
kind=held` and `yardr all kind=held depot=web` are two rows, though every
event of the second is one of the first. `--json` prints the same:
`subscribers`, `rows` (each with `marks` by subscriber and `reaches`) and
`reaches_no_one`.

## Groups as subscribers

A manual group can subscribe like a crew member (`yardr subscribe approvers
yardr --done`). Its subscriptions deliver to each of its **members that is
a crew member**, with the reason `<reason> via <group>` (e.g. `done via
approvers`). Scopes are worked out per member: `mentions` means that member
is @mentioned. Redefining the group keeps its subscriptions. A runner other
than manual cannot subscribe.

A group's subscriptions reach no person: a member who is a user
([users.md](users.md)) is told of what they subscribed to themselves
([below](#users-as-subscribers)), not of what their group did. Human members
are told in herdr when a bead reaches their manual group's stage (group
config `notify=true`).

## Users as subscribers

A user ([users.md](users.md)) subscribes as a crew member does, with the same
sources, scopes and where keys:

```sh
yardr subscribe ada yardr --done --where depot=yardr         # every bead of the depot that becomes done
yardr subscribe ada yardr --where stage=decide             # every bead that enters decide
yardr subscribe ada yardr --where kind=gave_up             # every session that gave up
yardr subscribe ada github --scope all --where conclusion=failure
yardr user show ada                                        # lists them
yardr unsubscribe ada yardr [--scope …] [--where …]
```

A user has no identity in a hook source and follows no thread there, so a
hook subscription defaults to `--scope all`; `mentions` and `involved` are
refused with `use --scope all` instead of being saved where they would
match nothing. Yardr subscriptions keep the default `all` as for crew members.

**Who may.** The user themselves and the operator. Nobody else, and no
crew member: a crew member deciding what a person is told is what notifications
are there to end, and the refusal says so:

```
yardmaster is a crew member, and a crew member does not decide what a person is told: bo subscribes themselves, or the operator (jo) does it for them
```

Another user is refused as `bo does not decide what jo is told: …`.

The check is the store's, on the command's actor
([users.md](users.md#who-is-acting)), so it holds for every client.

**A matching event is a notification**, not mail: a record of its own in the
user's inbox, with no sender and so nobody to answer. yardr writes it as the
event is logged. No model is called and no crew member is nudged or spends a
turn on it. Its text is one line, made from the event in the words `yardr
inbox` shows a crew member for the same delivery, with the bead's title after
it where there is a bead:

```
web-3cps closed (done) by yardmaster reason=merged 1a2b3c4 on "Chat: a row for the yard"
web-hk7z advanced (all) by yardmaster from=new to=decide on "Which port for the hook listener"
hook (all) github workflow_run completed by jane on "Fix the build": ci failure on main  https://github.com/…
```

The first two are `<bead> <kind> (<reason>) by <actor> <key=value …>`, the
reason being why it reached the user ([Scopes](#scopes)). A closed bead's
line carries the commit when the event has one: its own `commit`, or the
one its reason names, as the assembly's `merged <sha>` does. The third is a
hook delivery: source, type and action, who acted, the thread's title, the
text and the link. The event's part is cut at 300 characters and the title
at 120, with `…`; the link is never cut.

What differs from a crew member:

- A user is not told of what they did themselves, as a crew member is not.
- In a hook source a user has no identity and takes part in no thread, so
  only `--scope all` reaches them there. It is the default for a user, and
  a user's `mentions` or `involved` subscription to a hook source is
  refused (above). In the yardr source every scope
  works: `@ada` in a note mentions the user `ada`, and `involved` adds the
  beads they created, advanced or noted.
- There is no `--on` for a user, so yardr's events that are not about a
  bead reach no person this way.
- A subscription with `bead=<id>` ends when the bead closes, after the
  close is told, as a crew member's watch does.

The event records whom it notified as `notified` (user to reason), beside
`deliveries` for the crew, so a view that follows the stream knows to
read the notifications again.

**The bound.** A user's inbox keeps the newest 500 notifications. Nobody
acknowledges a stream, so when a 501st arrives the oldest is dropped, read
or not. Mail has no such bound. Removing a user removes their subscriptions
and notifications, and a later user of the same name starts with neither.

Reading and acknowledging them is in
[users.md](users.md#the-inbox), and [`yardr chat`](chat.md#the-yard) shows
them in a row of their own.

**Told outside the yard.** A user who names a source (`yardr user set
<name> notify=<source> env_file=<file>`) is sent what lands in their inbox
through it, to a phone: every notification of their subscriptions, and
their mail. So `yardr subscribe mckean yardr --where stage=decide` is "my
phone buzzes when a bead needs me". There is no second kind of subscription
for it and no switch on one: what a user subscribes to is what they are
told, 20 seconds after it arrived if it is still unread, everything due as
one message, at most 20 an hour. A notification is told as its bead and
kind (`web-hk7z advanced`) and its line; a hook delivery's without its
link. [users.md](users.md#told-outside-the-yard) has what is sent, when,
and what happens when the source fails; [pushover.md](pushover.md) sets one
up.

## `--on`

A crew member's `--on` kinds (default: the class `attention`,
[below](#the-class-attention)) keep working, as **implicit `yardr all kind=<k>`
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

`session_harness_error`: a session whose agent's harness stopped on an
error of its own and sits idle. Its `reason` is `at_capacity`,
`not_logged_in`, `rate_limited` or `other`, `line` is the error as the
harness printed it and `tail` the end of the pane
([groups.md](groups.md#a-harness-that-stopped-on-an-error)). A session
that hit `at_capacity` on its first prompt is given the prompt again by the
daemon first (`session_harness_retry`, one per try, in nobody's default
`--on`); its `session_harness_error` comes when the tries are used up and
says how many there were (`tries`). One that hit a usage limit
(`rate_limited`) is told of at once, with `wakes_at` when the daemon will
prompt it at the window's reset; that prompt is a `session_harness_retry`
too, and a prompt the harness did not take is a second
`session_harness_error` with `prompt_shown: false`. `session_idle`
carries the same `tail`, so the inbox shows what an idle agent's pane ends
in.

`move_refused`: a move (`yardr bead done`, `advance`, `close`, `hold`,
`release`) the yard refused to a session's agent, which may sit idle as if
it had worked. Its `reason` is `stale`, `not_allowed`, `not_member`,
`not_found` or `other`, `command` is what the agent ran and `error` what it
was told ([line.md](line.md#2-new-a-builder-implements-it)). Once
per bead, session and reason; a person's refused command logs none.

`session_prompt_gave_up`: a session whose agent never took its first prompt
([groups.md](groups.md#herdr-setup)). Its `hint` is the command that
gives the agent the prompt by hand, or, with `typed: true`, the Enter that
submits the prompt sitting in its input. A crew member whose `--on` is a
list copied before this kind joined the default does not hear it until its
`--on` names it, or the class.

`hook_rejected`: the webhook listener refused a delivery
([below](#a-refused-delivery)). It is in no crew member's default `--on`: add
it to the list of the crew member that should hear of it, `yardr crew set
<name> --on <its kinds>,hook_rejected` (`--on` replaces the list). The event
is about no bead, so a `yardr subscribe <name> yardr --where
kind=hook_rejected` does not carry it: subscriptions to yardr match bead
events, `--on` kinds every event.

To move a crew member to subscriptions, add `yardr subscribe <name> yardr
--where kind=<k>` with a `--where kind=` for each kind (one subscription,
[several values for one key](#several-values-for-one-key)) and drop those
kinds from its `--on`.

### The class `attention`

`attention` is the name of the kinds that need someone: work that needs a
decision, anything that went wrong, beads held, and beads that waited longer
for a manual group than its `wait` allows
([groups.md](groups.md#a-bead-that-waits-long)). In this release they
are `gave_up`, `session_blocked`, `unrouted`, `stranded`, `no_runner`,
`workspace_kept`, `crew_gave_up`, `runtime_down`, `session_idle`,
`session_aged`, `session_stalled`, `session_harness_error`,
`session_model_mismatch`, `session_prompt_gave_up`, `move_refused`, `held`,
`waited` and `nudge_held` (an agent's input holds text, or a dialog of its
harness, so nothing is typed there: see "Text in the input" in
[crew.md](crew.md)). The list is yardr's, one for the yard and every
release's own: no command changes it.

The name goes where a kind goes, and what is stored is the name:

- **In `--on`.** `yardr crew add` without `--on` gives a crew member
  `attention`, and `yardr crew show` prints `on attention`. Kinds may stand
  beside it: `--on attention,hook_rejected`.
- **In a filter of the yardr source.** `--where kind=attention` matches a
  bead event of any kind of the class, alone or with other keys and other
  kinds beside it: `yardr subscribe clerk yardr --where depot=web --where
  kind=attention` is what needs someone in one depot, and not in another.
  A user subscribes to it the same way. As every yardr subscription it
  carries the events of work beads only: `runtime_down`, which is about no
  bead, and `crew_gave_up`, which is on a crew member's journal, come
  through `--on` alone.
- **In a pack.** `hears: events: [attention]`, as the starter pack has it
  ([packs.md](packs.md)).

The name is looked up when an event is matched: for `--on` when the inbox
is read, for a filter when the event is logged. So a kind that joins the
class in a later release reaches every crew member and every subscription
that names the class, whenever they were made. For `--on` that includes the
events of that kind logged since the crew member last read its inbox, as
after any change of `--on`. An event in the log under a word an earlier
release used (`resident_gave_up`) is of the class as the kind it is today
(`crew_gave_up`).

A manual group has subscriptions and no `--on`, so it takes the class as a
filter: `yardr subscribe <group> yardr --where kind=attention` delivers the
bead events of the class to each of its members that is a crew member
(reason `all via <group>`). The events about no bead do not reach a group.

A crew member made before the class has in its `--on` the list of kinds
that was the default on the day it was made, and that list stays as it is:
a kind that joined the default later is not in it. yardr does not rewrite
such a list, because someone who took a kind out on purpose has the same
list, and the two cannot be told apart. `yardr doctor` names each crew
member whose `--on` holds every kind of the first default and not the
class, says which kinds of the class it does not hear, and gives the one
command that puts the class in their place and keeps the kinds it has
beside them ([doctor.md](doctor.md)):

```sh
yardr crew set yardmaster --on attention
```

## Hook sources

A webhook delivery (`yardr hook add`) becomes one `hook` event with a
normalized envelope. Who it reaches depends on the crew member's identity in the
source:

```sh
yardr crew identity yardmaster --source linear --id <user id> --key <profile key>
yardr subscribe yardmaster linear --scope involved
yardr subscribe triage linear --scope all --where type=issue --where action=create --where team=ENG
yardr thread follow yardmaster --source linear <issue id>    # participate without having acted
yardr thread list yardmaster
```

Where keys: `type`, `action` (the envelope), and `team`, `project`, `repo`,
`label` (fields the adapter exposes; any value may match). Once a crew member
acts in a thread (a Linear issue), answers there with `yardr reply`, or
opens it with `yardr thread open`
([crew.md](crew.md#opening-a-thread)), it participates there, and
`involved` also delivers what others do in it.
In a thread it takes part in it can also set the issue's status, when its
definition allows it: `yardr thread state`
([crew.md](crew.md#setting-a-threads-status)).

A hook also works the other way. With `--as <crew member>` and `--map
<type>:<stage>=<state>` it hears the yard's own bead events and moves the
issue a bead tracks when the bead enters a stage, as that crew member, with no
agent in between ([links.md](links.md#a-beads-stage-moves-the-issue)).
`yardr hook list` shows the map, and `yardr hook show <name>` the whole
definition.

An issue's description is re-sent whole with every update, so a mention there
counts once: yardr keeps who each issue's description mentioned and treats
only newly added names as mentions. A status or label change on an issue that
still mentions `@yardmaster` does not reach `yardmaster` as `mentioned` again
(it can still reach it as `assigned`, `participant` or `all`). The event keeps
the full list in `mentions`; `new_mentions` is what routing used. A comment is
the same: its creation counts every mention in it, an edit (Linear `update`,
GitHub `edited`) only the names it adds, and a deletion (`remove`, `deleted`)
tells no one and forgets the comment. The envelope's `entity` names what is
diffed: the issue or pull request, the comment, or a review.

### Setting up Linear

What runs today, from nothing to a crew member answering in Linear:

1. **The crew member's own Linear user.** Create a Linear OAuth application for
   the crew member and take its developer token: the token acts as the app's own
   user (the yardmaster's is called Yardr). A personal API key works too, but
   then the crew member speaks as that person.
2. **The token in its env_file**, never in the store:
   `LINEAR_API_KEY=…` in `~/.yardr/crew/<name>.env`, `chmod
   600`, `yardr crew set <name> env_file=…` (see
   [crew.md](crew.md#outbound-credentials)). Restart the crew member
   after changing it.
3. **Its identity**: the app user's id and profile key, so mentions,
   assignments, delegations and its own actions are recognized:
   `yardr crew identity <name> --source linear --id <user id> --key <profile key>`.
4. **A subscription**: `yardr subscribe <name> linear --scope involved`.
5. **The hook**: give the yard an address to take webhooks on,
   `yardr settings set hook_addr 127.0.0.1:8790` (the server takes it at its
   next start: `yardr server stop`, and the next command starts it; `yardr
   doctor` says what it listens on), and
   `yardr hook add linear --source linear --secret-file ~/.yardr/linear.secret`.
   One hook serves every crew member.
6. **The webhook**: in Linear, Settings → API → Webhooks, URL
   `https://<public host>/hooks/linear`, events Issues and Comments; put its
   signing secret in the secret file.
7. **The funnel**: something public in front of the hook listener, such as
   a tunnel or a reverse proxy of your own, which serves `https://<public
   host>` to the listener and nothing else.

A comment mentioning the crew member then lands in its inbox (`mentioned`), and
it answers with `yardr reply <seq>` and reads the thread with `yardr thread
show <seq>` ([crew.md](crew.md#speaking-in-sources)). GitHub is
the same with a `GITHUB_TOKEN` or a [GitHub App](#as-a-github-app), its
login as identity and the webhook below.

### GitHub

```sh
yardr hook add gh --source github --secret-file ~/.yardr/github.secret
yardr crew identity yardmaster --source github --key yardmaster   # its login, lowercase
yardr subscribe yardmaster github --scope involved
yardr subscribe yardmaster github --scope all --where train=yardr-sv6b --where conclusion=failure
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
crew member answers as the App's bot, `<slug>[bot]`, not under a person's
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
and give the crew member the key and its identity:

```sh
# in the crew member's env_file, in place of GITHUB_TOKEN
GITHUB_APP_ID=1234567            # the App ID, or its Client ID
GITHUB_APP_PRIVATE_KEY_FILE=~/.yardr/crew/yardr-app.pem   # mode 0600

yardr crew identity yardmaster --source github --key 'yardr[bot]'   # <slug>[bot], lowercase
```

[crew.md](crew.md#as-a-github-app) has the steps and what yardr
does with the key. Remove the webhooks of the repositories the App is
installed on once its own delivers: an App's delivery of an event is another
body than the repository webhook's, so the hook would record the event
twice.

GitHub has no mention of an App. `@yardr` in a comment notifies no one, and
links to the user named yardr if there is one; people write it anyway, as
they do for other Apps, and the App finds it in the comment's body. So does
yardr: a crew member whose github key is `yardr[bot]` is mentioned by `@yardr`
(and by `@yardr[bot]`). What the bot itself does has `yardr[bot]` as its
`actor.id` and stays out of the crew member's inbox; the user named yardr is
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
and `mentions`, so a crew member's github identity is its login in lowercase.
`@org/team` mentions, email addresses and mentions in code are not mentions.
Where keys, besides `type`, `action`, `repo` and `label`: `event`, `number`,
`state` (the issue's or pull request's, or the review's), `merged`,
`conclusion`, `workflow` (a run's workflow, or a check suite's app),
`head_branch`, `head_sha`, `head_repo`, `base`, `sender`, and `train`: the yardr train
when the head branch is `yard/<train>`, so `--where train=yardr-sv6b` follows a
train's pull request and CI without parsing branch names. `head_repo` is
owner/name of the repository the head branch is in: a fork's for a fork's
pull request or workflow run. A fork's branch may have the name of the
yard's own, and so the same `head_branch` and `train`; `--where
head_repo=<owner>/<repo>` with the yard's repository leaves the fork's out.
`sender` is what acted, as GitHub types it: `user`, `bot` (a GitHub App or
Actions, whose login ends in `[bot]`) or `organization`.

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
([crew.md](crew.md#outbound-credentials)): a regular file of mode
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

### A refused delivery

A delivery the listener does not take is answered with its status and logged
as a `hook_rejected` event, so that a source whose deliveries are refused can
be told from one that sends nothing:

```
yardr events          # … hook_rejected  hook:linear  count=1 hook=linear last=2026-10-06T08:24:01Z reason=unauthorized status=401
yardr hook list       # LAST ACCEPTED and LAST REFUSED, per hook
```

| reason | status | the delivery |
|---|---|---|
| `unknown_hook` | 404 | named no hook: a mistyped URL, a hook removed, or another yard's listener on the address |
| `unauthorized` | 401 | had no signature or token, a wrong one, or a timestamp too old |
| `bad_request` | 400 | was signed but is not what the source sends (no JSON, a missing header) |
| `too_large` | 413 | was larger than 1 MiB |
| `blocked` | 503 | came while the hook's secret file fails the rule (above) |
| `secret` | 500 | came while the secret file is empty |
| `read`, `store`, `unknown_source` | 400, 500 | could not be read or recorded |

The event carries `hook` (the name asked for, cut to 64 characters),
`reason`, `status`, `count` and `last`, and never the body, a header or the
secret. Anyone who reaches the listener can have it refuse them, so the
events are bounded:

- one name and reason log **one event in ten minutes**. The first refusal is
  logged at once, with `count` 1; those after it are counted, and the count
  is logged when the ten minutes end (within a minute of that), or with the
  next refusal after them, whichever comes first, and when the server stops.
  `count` is how many were refused since the pair's last event and `last`
  when the last of them was;
- of the names that are no hook's, **32 are told apart** at a time; a
  refusal under a further one is logged under `(other)`. A name is forgotten
  ten minutes after its last event when nothing was refused since. A hook's
  own name is always its own, so a scanner trying names does not hide a
  broken hook.

A duplicate is no refusal and logs nothing.

`yardr hook list` and `yardr hook show <name>` say when each hook last
accepted a delivery and when it last refused one, with the reason. The time
of the refusal is that of the hook's last `hook_rejected` event, so it can
trail a refusal by the ten minutes. Both times are kept on the hook, also
when it is added again. A hook whose last delivery was refused is a note on
`yardr doctor`'s hooks line; one with neither time has been sent nothing
this yard's server saw (then look at the source's own delivery log: the
delivery went elsewhere, or was not sent).

To have a crew member hear of it, add the kind to its [`--on`](#--on); it is in
no default.

Only the server's webhook listener records deliveries. The store socket has no
method to record one, nor to log an event of an arbitrary kind: clients log
dispatch decisions (`gave_up`, `session_blocked`, …) through a method that
accepts those kinds only.
