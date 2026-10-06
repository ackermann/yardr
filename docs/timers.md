# Timers: the yard tells someone later

A timer is a resident's or a user's request to the yard: tell someone this,
at that time, unless what I am waiting for happened first. It is a record in
the store and the daemon fires it, so it outlives the session that set it.

```sh
yardr timer add --in 40m "look at the deploy again"
yardr timer add --at 09:00 "read the night's CI"
yardr timer add --every 1d --for 14d --unless bead=yardr-1a2b "yardr-1a2b is still open"
yardr timer add --in 30m --to mckean --unless bead=yardr-1a2b,stage=review \
  "yardr-1a2b is stuck in new"         # a resident, for a user

yardr timer list                         # those you set and those that tell you
yardr timer list --all                   # the operator: every timer of the yard
yardr timer rm t3
```

**Prefer an event.** What can be heard as an event is heard when it happens,
with no clock: a subscription, or a bead's done (`yardr bead watch <id>
--done`, [subscriptions.md](subscriptions.md)). A timer is the fallback for
when the event does not come ("tell me if this is still not in review in 30
minutes"), or a check by the clock ("read the night's CI at nine").

## Setting one

`yardr timer add (--in <duration> | --at <time> | --every <duration>)
[--to <name>] [--unless bead=<id>[,stage=<stage>]] "<reason>"`

The reason is required. It is what the receiver reads when the timer fires.

| flag | when it fires |
|---|---|
| `--in 40m` | once, that long from now |
| `--at <time>` | once: RFC 3339 (`2026-10-06T09:00:00+02:00`), or `15:04` for the next time the local clock shows it |
| `--every 1d` | each period, first one period from now, for 7 days or `--for <duration>` |

Durations are Go's (`90s`, `40m`, `12h`) or whole days (`7d`).

## Who sets one, and for whom

A resident and a user set timers. An agent working a bead does not: what it
has to say goes on its bead.

`--to` names who is told; the default is whoever sets it.

| who sets it | for whom |
|---|---|
| a resident | itself, or a user of the yard (`yardr user list`) |
| a user | themselves |

Nobody sets a timer for another resident: mail it, and it sets its own if it
wants one. A user who wants another user to know something mails them.

Like every command, `yardr timer` goes by its actor (`--actor`, else
`$YARDR_ACTOR`, else the login name); yardr does not check who is at the
keyboard ([users.md](users.md#who-is-acting)).

## What a firing is

**To a resident** it is an event of kind `timer` in its inbox. The usual
nudge wakes the resident for it when it is idle
([residents.md](residents.md)):

```
   512 10-05 14:40  timer: look at the deploy again (set 10-05 14:00 by yardmaster).
```

**To a user** it is mail from whoever set it, whose text begins `timer:`. It
is in `yardr inbox` and in the conversation with that resident in
[`yardr chat`](chat.md), like any other mail, and `yardr mail --with <name>
--json` marks it with `timer`, the timer's id. Nothing nudges a person, and
the resident that set it is not woken: "tell mckean if this bead is stuck"
costs no session a turn.

A user's own timer is mail from themselves: `yardr inbox` shows it, and
`yardr chat`, which shows the conversations with residents, does not.

The text says the reason, who set the timer and when (in the time zone of
the yard's server), and, when they apply:

- how the awaited bead stands: `yardr-1a2b is still in new; this timer
  waited for it to reach review.`
- that it is late: `Late by 2h10m: the yard's daemon was not running when it
  was due.`
- that a recurring timer has ended: `This was the last; set it again if
  still wanted.`

## `--unless`: the event came first

`--unless bead=<id>` drops the timer when the bead closes before it fires.
`--unless bead=<id>,stage=<stage>` drops it when the bead has entered that
stage: it is in the stage, was in it (and moved on, or was sent back), is in
a stage its flow orders after it, or is closed.

A dropped timer tells its receiver nothing. The log has a `timer_dropped`
event with why (`yardr-1a2b is in review`).

The condition is read on every pass of the daemon, so a timer is dropped
when its event happens, not only when it would have been due, and leaves the
setter's limit then. A recurring timer with `--unless` fires each period
while the bead is short of what it waits for and ends when that holds.

`timer add` refuses a bead that is not in the yard, a stage the bead's flow
does not have, and a condition that already holds.

## Limits

| limit | default | where |
|---|---|---|
| live timers one setter may have | 5 | setting `timer_limit` |
| shortest `--every` | `1h` | setting `timer_min_every` |
| how far ahead a one-shot may be due | 30 days | fixed |
| how long a recurring timer runs | 7 days; `--for` up to 30 days | fixed |
| the reason | 1 KB | fixed |

`yardr settings set timer_limit 10` and `yardr settings set timer_min_every
30m` change the two settings, for timers set from then on. A refusal names
the limit it ran into.

A recurring timer's last firing says it was the last. Set it again if it is
still wanted.

## When the daemon was not running

Timers are fired by the daemon's pass. One that was due while no daemon ran
fires once on the first pass of the next, marked late with by how much. A
recurring timer fires once for all the periods it missed, then at its next
period: time missed is one firing, never one per period.

A firing counts as late from one minute after it was due; the daemon wakes
for the next timer, so a running one fires within about a second.

A timer whose receiver has left the yard (a user removed, a resident
deleted) is dropped, with `timer_dropped`.

## Where timers are seen

- `yardr timer list`: the timers you set and those that tell you; `--all`,
  for the operator, every timer. `--json` prints the records: `id`, `by`,
  `to`, `to_user`, `reason`, `set_at`, `due`, `every`, `ends`, `unless`,
  `fired`.
- `yardr prime --resident <name>`: a resident's live timers, under `##
  Timers`, so its next session knows what it asked for.
- `yardr resident show <name>` and `yardr user show <name>`.

`yardr timer rm <id>` removes a timer you set or that tells you; the
operator removes any.

## Events

| event | when | carries |
|---|---|---|
| `timer_set` | a timer was set | `timer`, `to`, `due`, `reason`, `every`, `ends`, `unless` |
| `timer` | it fired | `timer`, `by`, `to`, `reason`, `text`, `set_at`, `late`, `last`, `unless` |
| `timer_dropped` | its `--unless` held, or its receiver is gone | `timer`, `by`, `to`, `reason`, `why` |
| `timer_removed` | `yardr timer rm` | `timer`, `by`, `to`, `reason` |

A firing to a user is two events: the `mail` the user reads (with `timer`,
the id) and the `timer` event. Of these the [web feed](web.md) shows the
firing, once; `yardr events` shows all of them.

From Go: `store.API`'s `AddTimer`, `Timers`, `RemoveTimer`, and
`FireTimers`, which the daemon calls on each pass with its clock.
