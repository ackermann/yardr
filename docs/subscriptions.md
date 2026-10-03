# Subscriptions and notifications

A resident hears of things through its inbox (`yardr inbox`). Mail to it
always lands there. Everything else arrives because something subscribed it:

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
yardr subscribe mayor yardr --done --where rig=yard --where label=ui
yardr subscribe mayor yardr --scope involved               # beads it created, advanced or noted, and events @mentioning it
yardr bead watch yard-k3f9 --resident mayor --done         # this one bead, until it closes
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
| `involved` | mentions, plus deliveries that assign it, plus threads it participates in | mentions, plus events on beads it created, advanced or noted |

The default is `involved` for a hook source and `all` for yardr, where the
where filters usually do the narrowing.

Reasons, from most to least specific: `done` (a done subscription matched),
`mentioned`, `assigned`, `participant`, `all`. When several subscriptions
match, the most specific reason is recorded.

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

Human members get nothing in an inbox yet. They are told in herdr when a bead
reaches their manual group's stage (group config `notify=true`).
Delivering subscriptions to humans waits for channels (*planned, yard-1*).

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
acts in a thread (a Linear issue), or answers there with `yardr reply`, it
participates there, and `involved` also delivers what others do in it.

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
   `LINEAR_API_KEY=…` in `~/.config/yardr/residents/<name>.env`, `chmod
   600`, `yardr resident add <name> --set env_file=…` (see
   [residents.md](residents.md#outbound-credentials)). Restart the resident
   after changing it.
3. **Its identity**: the app user's id and profile key, so mentions,
   assignments and its own actions are recognized:
   `yardr resident identity <name> --source linear --id <user id> --key <profile key>`.
4. **A subscription**: `yardr subscribe <name> linear --scope involved`.
5. **The hook**: start the server with `YARDR_HOOK_ADDR=127.0.0.1:8790` and
   `yardr hook add linear --source linear --secret-file ~/.config/yardr/linear.secret`.
   One hook serves every resident.
6. **The webhook**: in Linear, Settings → API → Webhooks, URL
   `https://<public host>/hooks/linear`, events Issues and Comments; put its
   signing secret in the secret file.
7. **The funnel**: something public in front of the hook listener. Ours is
   Tailscale Funnel (`tailscale funnel --bg 8790`), which serves
   `https://<machine>.<tailnet>.ts.net` to the listener and nothing else.

A comment mentioning the resident then lands in its inbox (`mentioned`), and
it answers with `yardr reply <seq>` and reads the thread with `yardr thread
show <seq>` ([residents.md](residents.md#speaking-in-sources)). GitHub is
the same with a `GITHUB_TOKEN` (or the pane's `gh` login), its login as
identity and the webhook below.

### GitHub

```sh
yardr hook add gh --source github --secret-file ~/.config/yardr/github.secret
yardr resident identity mayor --source github --key mayor   # its login, lowercase
yardr subscribe mayor github --scope involved
yardr subscribe mayor github --scope all --where epic=yard-sv6b --where conclusion=failure
```

On GitHub, in the repository's **Settings → Webhooks → Add webhook**:

- **Payload URL:** `https://<your host>/hooks/gh`, the hook listener
  (`YARDR_HOOK_ADDR`) behind whatever exposes it.
- **Content type:** `application/json`. The form-encoded type is refused.
- **Secret:** the contents of the `--secret-file`. Each delivery is checked
  against `X-Hub-Signature-256` (HMAC-SHA256 of the body).
- **Events:** *Let me select individual events*, then tick Pull requests,
  Pull request reviews, Issue comments, Issues, Workflow runs and Check
  suites. Other events are recorded too, but with only `event`, `action` and
  `repo` and no thread.

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
`head_branch`, `head_sha`, `base`, and `epic`: the yardr epic
when the head branch is `yard/<epic>`, so `--where epic=yard-sv6b` follows an
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
- **GitHub:** the `X-GitHub-Delivery` header, per hook. A redelivery keeps
  its id; bodies carry no timestamp, so the body is not a key.
- **token:** the `Yard-Delivery` header, per hook, when the sender sets one.
  The body is not a key: a cron job may send `backup ok` every hour.

A duplicate is answered 200 with `{"duplicate": true, "seq": <first>}`, so the
sender stops retrying, and leaves no event.

Only the server's webhook listener records deliveries. The store socket has no
method to record one, nor to log an event of an arbitrary kind: clients log
dispatch decisions (`gave_up`, `session_blocked`, …) through a method that
accepts those kinds only.

## Planned

- `outcome` as a where key, once beads carry outcomes (*planned, yard-qexg*).
