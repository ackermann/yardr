# Telemetry

yardr's telemetry switch records consent for one daily batch of counts
and fixed names to its PostHog project, answering the four questions
below. It is off unless the operator turns it on, with one switch, and
`yardr telemetry show` prints the exact next batch before anything is
sent.

## The switch

`yardr telemetry on` turns it on, `yardr telemetry off` turns it off,
`yardr telemetry show` previews the next batch. There is nothing else:
no per-category choice, no environment variable (a variable would let a
session's environment consent for a yard whose operator never did), and
no flag. The generic `yardr settings set telemetry` refuses the key and
names these commands, so a raw setting write cannot skip the consent
bookkeeping. Only the operator changes it, and no run ask may propose
it, so no agent can turn its own yard's telemetry on.

This build records consent and previews the batch, but does not send
telemetry yet. When a build includes the sender, the daemon reads the
setting on every pass, so a change holds on the next pass with no
restart. A yard whose daemon never runs sends nothing. The batch goes to
`https://eu.i.posthog.com/batch/`,
the shared EU project's capture endpoint, which is compiled in, never
configured, so no setting can point a yard's telemetry elsewhere.

The interactive onboarding asks once, after the yard exists, defaulting
to No and writing the answer either way, so a completed onboarding never
asks again. A non-interactive `yardr init --harness <kind>` never asks
and leaves it off.

## The four questions

Only what answers these is sent:

1. Which yardr versions and OS are running.
2. Which harnesses and models do the work.
3. Where beads fail: `gave_up`, held, landing failures, as counts by kind.
4. Do yards keep running: active after 1, 7 and 30 days.

## What each batch carries

One batch covers one UTC sending day, with that day's runtime snapshot
and up to seven completed source days of counts. Every event carries
the yard's install id as `distinct_id`, a `timestamp` day marker and a
stable `uuid`. Every event sets `$process_person_profile` so deletion
by install id does not depend on a project default. The names start
with `yardr_` so they stand apart from the project's other events.

`yardr_daily`, once per sending day on a self-run yard, answers
questions 1 and 4:

| property | value |
|---|---|
| `version` | the release tag, or `development` (below) |
| `os` | `darwin`, `linux`, `windows`, or `other` |
| `telemetry_age_days` | whole UTC days from first consent through the sending day, 0 or more |

A tag of the closed shape `v1.2.3`, optionally with one `-alpha.N`,
`-beta.N` or `-rc.N` suffix, is sent as it stands. Everything else is
the one bucket `development`. The revision is never sent.

`yardr_harness_day`, one per harness and model pair with bead sessions
claimed on a source day, answers question 2:

| property | value |
|---|---|
| `kind` | the harness kind, or `other` |
| `model` | the model id, `other`, or `default` (below) |
| `sessions_claimed` | bead sessions of that pair claimed that day |
| `hosted` | whether this is a hosted yard |

`yardr_crew_day`, one per pair with crew sessions running when the batch
is built, also answers question 2:

| property | value |
|---|---|
| `kind` | the harness kind, or `other` |
| `model` | the model id, `other`, or `default` |
| `running_crew_sessions` | live crew sessions of that pair now |
| `hosted` | whether this is a hosted yard |

A kind yardr has a file for is sent as it stands; anything else is
`other`. A session with no kind (a command, a person, another yard) is
not counted at all. A model is sent under its own name only when it is
exactly one of that harness's checked-in offered ids. Any other
configured value is `other`, and a blank model — the harness's own
default was used — is `default`.

`yardr_failure_day`, one per failure kind seen on a source day, answers
question 3:

| property | value |
|---|---|
| `kind` | `gave_up`, `held`, or `landing_failed` |
| `count` | events of that kind that day |
| `hosted` | whether this is a hosted yard |

The kinds are lenses, not a partition: a bead the dispatcher gave up on
is also held, so it stands in both counts. `landing_failed` counts only
a move out of the conventional `approved` stage with outcome `failed`;
stage names are checked, never sent. Not counted: a review's send-back,
the quiet whole-suite run, a prompt that never arrived, and upgrade
failures.

Nothing else travels. No property ever carries a bead id or title, a
depot, group, crew, user, peer or yard name, a stage or flow name, a
path, a URL, a command, an error's words, a note's or prompt's text, an
actor, a session id, the revision, or the yard's name.

## Hosted yards

A hosted yard — one the hosting service made and runs — sends no
`yardr_daily`: the service already knows its version and whether it is
reached, so questions 1 and 4 need no client event. What it cannot see
is inside the yard, so hosted yards count questions 2 and 3 with
everyone else's, each event carrying `hosted` for the breakdown. A
hosted yard with no eligible counts and no running crew sends no batch.

Hosted yards send questions 2 and 3 only after their customer turns the
switch on in the browser or in footplate; until that control lands they
stay off. The provisioner never sets the switch. No account, handle or
yard name the service knows is ever sent.

## The install id

The install id is a random UUID, minted from cryptographic randomness
when telemetry is first turned on — never derived from the machine, the
home's path, or anything in the yard — and kept with its consent
bookkeeping in `<home>/telemetry.json` (mode 0600), beside the yard's
other state. It never changes while the file stands: across upgrades,
restores of the store, and off and on again.

This id is pseudonymous, not anonymous: anyone you give it to can find
this yard's events, and giving it is your act.

PostHog keeps an id-only person profile for it — no name, no email, no
properties set — so a yard's data is deleted by install id. The id is
printed by `on`, `off` and `show` so it can be pasted into a
conversation; it appears nowhere else: not in the web view, mail,
`doctor` or `status` output.

## Seeing a batch

`yardr telemetry show` prints the install id and the exact request the
daemon would send from the current state, without sending it or
advancing anything: the sending-day snapshot and any eligible completed
source days. It previews from current state, and the yard can change
before the daemon's next pass, so a later batch may differ. It never
previews a day from before consent. While telemetry is off, after
today's batch was attempted, or with nothing to send, it prints the id
and says there is no batch. `--json` prints the same object the text
renders.

With the id shortened inside the batch and the key shortened, a batch
looks like this:

```text
install id: 3f6d1c2e-9a4b-4e7a-b5c1-2d8f0a6e4b90
This id is pseudonymous, not anonymous: anyone you give it to can find this yard's events, and giving it is your act.

telemetry: on (since 2026-10-26)
next request: sending day 2026-10-28 (UTC); source day 2026-10-27
{
  "api_key": "phc_…",
  "batch": [
    {"event": "yardr_daily", "distinct_id": "3f6d…",
     "timestamp": "2026-10-28T00:00:00Z",
     "uuid": "0e3c8b2a-1432-4a51-9f39-1bc7a1b1d001",
     "properties": {"$process_person_profile": true,
       "version": "v1.2.3", "os": "darwin",
       "telemetry_age_days": 2}}
  ]
}

To have this yard's data deleted, send the project this install id.
```

## Stopping, and eligible days

`yardr telemetry off` records the withdrawal and prevents new requests
from starting on the next daemon pass; a request already submitted may
still complete. The install id is kept, so turning on again is the same
yard.

A source day is counted only when telemetry was enabled for that whole
UTC day, so activity from before consent is never included. Turning on
starts eligibility on the next UTC day; turning off and on again starts
a new eligible day and drops any unsent days from the earlier consent
period. Repeating `on` while already on changes nothing.

To forget the yard entirely, turn off, delete `<home>/telemetry.json`,
and turn on. A new id is minted, and the new yard of data is unlinkable
to the old one by anything yardr sends. Deleting the file is a complete
local forget. A store backup cannot resurrect the id, since the file
stands outside the store.

## Deletion

To have a yard's data deleted, send the project its install id, in the
conversation the id exists for or any channel already used with the
project. The id is sufficient and no second key is needed. The project
deletes the id-only profile and its linked events through PostHog's
person deletion API, which runs asynchronously. Two honest limits travel
with that promise. Deletion removes what PostHog holds, not an aggregate
a person already read. And while a deletion is in flight the id must not
be reused, so a yard that asked for deletion and turns telemetry on
again first forgets the old id locally, as above.

## Retention

PostHog keeps the raw events for 12 months, then deletes them, with only
aggregates made before deletion kept longer. A year covers the 30-day
cohorts many times over and every version's working life so far.
