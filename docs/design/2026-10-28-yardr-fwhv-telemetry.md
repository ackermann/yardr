# Opt-in telemetry: what yardr measures, what it sends, and the one switch

Design yardr-fwhv, proposed 2026-10-28. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead. No code,
no flag and no user doc lands with this design: each piece of section 3 becomes
its own bead (section 6).

Terms used below. A *yard* is one installation of yardr: its store, daemon,
crew and depots, in one home (`~/.yardr` unless `--home` says otherwise). The
*daemon* is the long-lived dispatcher (`internal/dispatch`) whose pass wakes on
a committed write and re-reads state. An *event* is one entry of the store's
append-only log (`store.Event`, `internal/store/events.go`): a kind, an actor,
a data map and a time. A *session* is one agent working one bead in one stage
(`store.Session`, `internal/store/routing.go`), stamped at its claim with the
harness *kind* and *model* it started with. A *harness* is the agent program
herdr starts (claude, codex, muse, …), described by `harnesses/<kind>.yaml`
through `internal/agents`. The *operator* is the person who runs the yard.
*PostHog* is the analytics service the events are sent to; a `phc_` key is its
public, write-only project key. *Hosted yards* are yards the hosting service
makes and runs for a customer (`hosted` setting, design yardr-bwm2); the
*edge* is that service's router in front of them.

## 1. The question

mckean, 28 Oct 2026 (mails 105771, 105805): opt-in, anonymized-as-far-as-it-goes
telemetry before release, sent to PostHog; friends of the project would enable
it. This supersedes the "no telemetry" of the 24 Oct launch measures
(yardr-4b4j Q6): those measures (landed mails, ten conversations) stay;
telemetry adds the silent users who never write in.

Decided by mckean on this bead (asks 15536 and 15578, 28 Oct), and written
below as decided, not re-argued:

- **One global on/off switch.** No per-category choices, no per-event toggles.
- **Four questions, and only what answers them is sent:**
  1. Which yardr versions and OS are running.
  2. Which harnesses and models do the work.
  3. Where beads fail: `gave_up`, held, landing failures, as counts by kind.
  4. Do yards keep running: active after 1, 7 and 30 days.
- **The install id is visible to its user**: a command prints it, so a user
  can paste it into a conversation and mckean can find their data when
  reviewing. The consequence is stated wherever the id is shown (section
  3.5): the id is *pseudonymous, not anonymous*, and sharing it is the
  user's act.

What this document settles: the event list and every property on it (3.1);
where in yardr each count comes from, and when it is sent (3.2, 3.3);
consent — off by default, the one switch, the init question, and the show
command that prints exactly the next batch (3.4); identity, deletion and
what GDPR asks of a pseudonymous id (3.5); the PostHog project (3.3); and
what hosted yards already show server-side, so the client does not
duplicate it (3.6). Two things are left to mckean as open questions with
options (section 5): how long PostHog keeps the raw events, and whether
hosted yards send at all.

## 2. What exists today

Everything in this section can be opened.

**The yard already records everything the four questions ask about, as
events, and keeps them 90 days.** The store's log is append-only, ordered
by `Seq`, and read by kind through an index (`EventsOfKind`,
`internal/store/events.go`). `Prune` keeps events for 90 days by default
(`DefaultRetention`, `internal/store/prune.go`). Nothing needs a new
instrumentation point: section 3.2's builder only reads this log and the
session records. Three existing mechanisms already prove the shapes this
design reuses. The daemon's dated store copies are a settings-driven,
once-per-period daemon job whose due-ness is read from what is on disk, so
a restarted daemon makes the overdue one on its first pass, and whose
failure is told once and retried with doubling backoff (`backups`,
`internal/dispatch/backups.go`). The OpenTelemetry exporter
(`docs/otel.md`) already sends yard events off the machine — but it is a
command a person runs against their own collector, it sends handles and
names by default (beads, depots, actors), and yardr itself "turns no
harness's telemetry on": it is the precedent for hand-writing an exporter
with no SDK (`internal/otel` writes OTLP/HTTP JSON itself), not a channel
this design may reuse. And `eventDataTold` (`internal/store/eventtold.go`)
is the yard's own allow-list discipline for what may be told outside: a
kind tells only the keys its row lists, and a key nobody listed is held.
Section 3.1 holds the telemetry payload to the same discipline, at the
builder, with a shorter list.

**Versions and OS.** A build names itself two ways (`internal/version`):
the release *tag* (`version.Tag()`, `v1.2.3`, stamped by
`contrib/release/build` with `-ldflags -X`, empty for `go install` and
`go build`) and the *revision* (a 12-character VCS hash, `-modified` when
dirty). `version.Describe()` joins them for people. The daemon already
logs what it runs: `daemon_started` carries `to: version.Show(to)` and
`daemon_replaced` carries `from`/`to` (`noteStart`,
`internal/dispatch/dispatch.go`). Question 1 asks which *versions* are
running, so section 3.1 sends the tag and buckets everything without one;
the revision is never sent (it fingerprints one build, and no question
needs it). The OS is `runtime.GOOS`; architecture is deliberately not
sent because none of the four questions asks for it. The codebase already
branches on the platform (`internal/cli/needs.go` keeps `goos`/`goarch`
vars).

**Harnesses and models.** A session is stamped at its claim with the kind
and model it starts with, resolved then from the group's config and the
yard's `default_kind` (`Claim`, `internal/store/routing.go`; `Group.Kind`),
and the `claimed` event carries both (`kind`, `model` in its
`eventDataTold` row). A crew member's kind and model are derived on
reading from its config (`CrewMember.Kind`/`Model`,
`internal/store/crew.go`). Kinds are a closed vocabulary: the kinds yardr
has a file for plus herdr's kinds (`agents.Kinds()`, `agents.IsKind`,
`internal/agents/models.go`). Models are not closed: a group's `model`
config is a string the operator typed (`ConfigKeys`, `internal/store/configkeys.go`),
the harness file only *suggests* ids (`Harness.Offers`), and init takes
any id. That difference decides the bucketing in section 3.1. Sessions of
the `exec`, `manual` and `peer` runners have no kind and no model: they
run a command, a person, or another yard, and question 2 does not ask
about them.

**Failures.** The three failure kinds the bead names already exist as
yard facts. `gave_up` is a dispatch event (`DispatchKinds`,
`internal/store/events.go`): after `MaxAttempts` (3) failed or died
sessions in one stage the dispatcher holds the bead and logs `gave_up`
with its failure count. A hold is the bead's `Hold` flag with a `held`
event (`SetHold`, `internal/store/beads.go`); a `gave_up` bead is also
held, so the two kinds are lenses, not a partition (section 3.1 says so
where they are counted). A *landing failure* is a move: every move logs
`advanced` with `from`, `to` and `outcome`, and the default flow's
`approved` stage leaves by outcome `failed` when the tests are red on
what would land or the branch no longer applies
(`packs/starter/flows/default/approved.md`). The review send-backs
(`changes`) are not landings and are not counted. The whole-suite quiet
run's `quiet_failed` is about a depot's base, not a bead's landing, and
is not counted either; both exclusions are named in section 3.1. Error
text, reasons and hints exist on several of these events today — and are
exactly what never leaves (section 3.1's never-list).

**Settings, and who may change them.** A yard setting is one row of
`KnownSettings` (`internal/store/settings.go`); a key that is not there is
refused by `SetSetting`, read by nothing, and listed by
`yardr settings get`. Settings are the operator's alone to change
(`yardr settings set` refuses an agent), and a new key defaults to the
`Alone` ask class: the operator types it, no run ask may propose it
(`settingAsk`). The daemon reads settings on every pass, so a change
holds on the next pass with no restart (`backups` above is the working
example). That is the whole machinery the one switch needs: one new
setting, no new store, no new gate.

**The daemon is the only process that is always there.** It wakes on a
committed write, re-reads state, and its pass already carries the yard's
other periodic work (timers, backups, the quiet run's trigger). A yard
whose daemon never runs sends nothing and is counted nowhere; that is
true of backups today and is stated for telemetry in section 3.3, not
hidden.

**Hosted yards are already observed server-side.** The hosting service
holds the account (Kratos identity, email, handle), the user-to-yard map
row (backend, target, state, when the yard was made), and sees every
request its router checks a session for, every hook delivery it routes,
and the yard image's pinned `versions.txt` — so for a hosted yard the
service already knows it exists, when it was made, whether it is reached,
and which yardr build the image carries (design yardr-bwm2, sections 3.2
and 3.3). What it cannot see is inside the yard: which harnesses and
models work, and where beads fail. Section 3.6 keeps the client from
re-answering the first two questions for hosted yards.

**The PostHog project already exists.** Footplate's feedback goes to a
PostHog Cloud EU project (bead footplate-ps5h in the footplate depot)
with its public `phc_` key hard-coded in the client (mckean, note 15552
on this bead). Footplate has no telemetry of its own; its feedback form
is that project's only current traffic. PostHog Cloud EU is hosted in
Frankfurt, its capture endpoints are `https://eu.i.posthog.com/i/v0/e/`
(single) and `https://eu.i.posthog.com/batch/` (batch), both POST-only
and keyed by the public project token. PostHog documents that IP capture
defaults off for new EU projects, but this is an existing project, so
that default proves nothing about its current setting. T2 must verify
that this project's IP capture is off before the first send; the endpoint
still sees the connecting address, as any endpoint does. PostHog's
[capture API](https://posthog.com/docs/api/capture) documents the batch
shape, and its [IP capture guidance](https://posthog.com/docs/privacy/gdpr-compliance)
describes the EU default. PostHog's
[bulk-delete API](https://posthog.com/docs/api/persons) accepts
distinct IDs and can delete linked events asynchronously. Section 3.5
uses an id-only profile so its deletion path applies to yardr's events.

## 3. The proposal

Four events, one switch, one daily batch. No new table, no new store, no
SDK: the daemon gains one pass step beside `backups`, the store gains one
setting, and the home gains one small state file. The same pure builder
constructs the batch for `yardr telemetry show` and for the daemon; show
prints the request the daemon would send from the current state, without
sending it or advancing any cursor.

### 3.1. The event list: four events, counts and fixed names only

One batch is one UTC sending day `S` (3.3). It contains that day's
runtime snapshot and up to seven completed source-day aggregates `D`.
Every event carries the yard's install id as PostHog's `distinct_id`.
PostHog creates an id-only person profile so deletion by `distinct_id`
can remove the linked events; yardr never sends `$set` or `$set_once`,
and supplies no person properties beyond the random id. PostHog may
maintain its own profile timestamps. Every other value below travels as
an ordinary event property. Every event explicitly
sets the reserved `$process_person_profile` property to `true` to make
that deletion path independent of a project default. Event
names use a `yardr_` prefix, so they stand apart from footplate's
feedback events in the shared project (3.3). Every event also has a
top-level `timestamp` and a stable `uuid`, which are envelope fields,
not properties. The daily and crew snapshots use a UTC day marker on
sending day `S`: 00:00 UTC unless the latest consent grant was on `S`,
in which case they use that grant's timestamp so no event is dated
before consent. Harness and failure counts use 12:00 UTC on source day
`D`. These are day markers, not claims about the exact send time. UUIDs
are derived from the install id, event name, day and fixed dimensions.
Rebuilding the same event preserves its timestamp and UUID, so
PostHog can deduplicate a source-day event in a later batch after a
response was lost. The project timezone is UTC so reports use the same
day boundary as the builder.

| Event | When | Answers |
|---|---|---|
| `yardr_daily` | once per self-run sending day `S` | Q1, Q4 |
| `yardr_harness_day` | once per (kind, model) pair with sessions claimed on source day `D` | Q2 |
| `yardr_crew_day` | once per (kind, model) pair with crew sessions running on sending day `S` | Q2 |
| `yardr_failure_day` | once per failure kind seen on source day `D` | Q3 |

**`yardr_daily`** properties, all fixed names or counts:

| Property | Value | From |
|---|---|---|
| `version` | the release tag, or a bucket (below) | `version.Tag()` |
| `os` | `darwin`, `linux`, `windows`, or `other` | `runtime.GOOS` |
| `telemetry_age_days` | whole UTC days from first consent date through sending day `S`, 0 or more | the state file (3.5) |

The version bucketing is the rule, not an example: a tag matching
`v<major>.<minor>.<patch>`, optionally with one pre-release suffix of the
fixed shape `-alpha.<n>`, `-beta.<n>` or `-rc.<n>` (`v1.2.3-rc.1`), is
sent as it stands; **everything else — `go install` pseudo-versions,
devel builds, unknown, and any other suffix — is the one bucket
`development`.** The tag is whatever `-ldflags -X` stamped, so whoever
builds the binary chooses it; a free-form suffix (`v1.2.3-alice-laptop`)
would carry their words, and only the closed shape keeps it a fixed
name. The revision
is never sent, in whole or in part. The presence of a `yardr_daily` for
a self-run `distinct_id` on a day is the activity record question 4
counts. Hosted yards do not send `yardr_daily`; the hosting service is
the source for their version and activity. The daily event's
properties describe the running build and yard on sending day `S`, not
on a backfilled source day, and `telemetry_age_days` makes the 1/7/30-day
cohorts a filter rather than a computation over first-seen dates. The
age starts at the first consent grant, even if the yard was installed
earlier; forgetting the id creates a new cohort. A
failed or missed send can leave a gap in this activity record; the next
batch does not invent an active day for which no request was sent.

**`yardr_harness_day`** properties: `kind` (a harness kind, bucketed),
`model` (a model id, bucketed), `sessions_claimed` (bead sessions
of that pair claimed on source day `D`), and `hosted` (`true` or
`false`, from the setting at build time on sending day `S`). The
`hosted` value on a backfilled count is the current setting, not a
historical value for `D`. One event per pair, only when the count is
above 0.

**`yardr_crew_day`** properties: `kind`, `model` (bucketed by the same
rules), `running_crew_sessions` (the count of live crew sessions of
that pair at build time on sending day `S`), and `hosted` (`true` or
`false`, from the setting on `S`). It counts the line session
and each live conversation session whose `CrewSession.State` is
`running`; it does not count enabled-but-stopped members or starting
sessions. This current snapshot captures always-on work that has no
bead claim. One event per pair, omitted when the count is 0; it is never
backfilled onto a historical day.

- *Kind bucketing:* a kind in `agents.Kinds()` (a filed kind or a herdr
  kind) is sent as it stands; anything else is `other`; a session with
  no kind (exec, manual, peer) is not counted at all.
- *Model bucketing:* only an exact model id in that harness's
  checked-in `Offers` list is sent under its own name. Those ids are
  fixed product names; labels are never sent. **Any other non-empty
  configured value is `other`, and an empty model — the harness's own
  default was used — is `default`.** `Offers` is a short suggested list,
  so a newly configured or dynamically discovered model stays in
  `other` until a yardr change names its id there. The config value
  itself is arbitrary text, and a character-shape check cannot
  distinguish a model id from a short name or word a person typed;
  exact membership is the boundary that keeps that value from leaving.

**`yardr_failure_day`** properties: `kind` — one of `gave_up`, `held`,
`landing_failed` — `count`, the events of that kind that day, and
`hosted` (`true` or `false`, from the setting on `S`). One event per
kind, omitted at 0. The kinds are lenses, not a partition, and
the document says so wherever they are charted: a bead the dispatcher
gave up on is also held, so it stands in both counts; `landing_failed`
counts only an `advanced` move out of the conventional `approved` stage
with outcome `failed`. A failed transition from another stage is not a
landing failure. Stage names are not properties: the source stage is
checked only to classify the existing move, and no stage name is sent.

**The never-list.** No property, on any event, ever carries: a bead id
or title, a depot, group, crew, user, peer or yard name, a stage or flow
name, a path, a URL, a command, an error's words, a note's or a prompt's
text, an actor, a session id, the revision, or the yard's name. The
builder constructs each event from the allow-list above and from nothing
else — the discipline of `eventDataTold` applied at the one place the
payload is made, so a future event kind or data key reaches PostHog only
by being added to this section first. The payload's own test (T1,
section 6) holds that line mechanically: a fixture yard whose beads,
depots, groups and models are named with sentinel words must produce a
batch containing none of them.

### 3.2. Where each count comes from

The pure builder takes a sending day `S`, a range of completed source
days `D`, the store and the state file. It reads:

- **`yardr_daily`**: only for a self-run yard. Nothing comes from the
  log: the running build's tag, the process's `runtime.GOOS`, and the
  state's minted date, all as seen on `S`. A yard that upgrades between
  sends reports the build that sends that day's batch; versions are read
  at that grain and no finer grain is promised. Hosted yards omit this
  event because their service already knows the version and activity.
- **`yardr_crew_day`**: the current crew rows (`Crew`), grouped by their
  derived `Kind`/`Model`. Count the line session and each conversation
  session whose `CrewSession.State` is `running`; do not count enabled
  but stopped members or sessions starting at build time. This is a
  current runtime snapshot for `S`, not historical data.
- **`yardr_harness_day`, bead sessions**: each source day's `claimed` events
  (`EventsOfKind`), grouped by their `kind` and `model` data, which
  `Claim` stamps from the group as it stands at the claim. A model a
  group stops using disappears from the next day; nothing is re-read
  from config for work already claimed.
- **`yardr_failure_day`**: each source day's `gave_up` events (dispatch kind,
  counted as events); the day's `held` events (one per hold placed);
  the day's `advanced` events whose `from` is `approved` and whose
  `outcome` is `failed`. A failed transition from another stage is not
  counted as a landing failure. Not counted, by name: `changes` (a
  review's send-back, not
  a landing), `quiet_failed` (the base's whole-suite run; it has no
  bead and answers a different worry), `session_prompt_gave_up` (a
  prompt that never arrived, not a bead that failed), and `install_failed`/
  `rolled_back` (upgrade failures, already told to the operator by their
  own subscriptions; no question asks about them).

The builder includes only event timestamps within each source day `D`.
Source days are UTC days throughout, so a yard's day does not drift with
its timezone. A daemon that was down can rebuild eligible source-day
counts from the 90-day log, capped to the most recent seven completed
days; runtime snapshots always describe the day on which the batch is
built.

### 3.3. When it is sent: one daily batch from the daemon

The daemon gains one pass step, `telemetry`, after `backups` in
`Reconcile`. The step reads settings on every pass, as `backups` does,
so a switch change holds from the next pass without a restart. It:

1. Does nothing when `telemetry` is off. Turning off starts no new
   request; a request already submitted may still complete. A later
   `on` moves `eligible_from` forward and discards any unsent source
   days from before the new consent period.
2. Makes at most one POST attempt per UTC sending day `S`, recorded as
   `last_attempt_day`, persisted before the HTTP request begins so a
   daemon restart cannot make a second attempt that day. A same-day pass
   after an attempt does nothing. For a self-run yard, the batch
   contains `yardr_daily`; every yard may also include current
   `yardr_crew_day` snapshots for `S` and eligible
   `yardr_harness_day` and `yardr_failure_day` source-day aggregates.
   A hosted yard omits `yardr_daily`; if it has no crew snapshot or
   source-day count event, there is no request to send. The first source
   day is the
   later of `eligible_from` and the day after `last_day_sent` (or
   `eligible_from` if none has been sent); the last is the most recent
   completed UTC day. If that range is longer than seven days, it drops
   the older part, logs the range once, and includes only the latest
   seven. It never includes a day before consent or an incomplete day.
3. POSTs `{api_key, batch: [...]}` to
   `https://eu.i.posthog.com/batch/` with a 10-second timeout,
   synchronously in the pass. One bounded call per UTC day is the cost;
   a failed attempt logs one warning containing only the HTTP status and
   a bounded error class: never the request body, response body,
   install id or project token. It is not retried until the next UTC
   day. The next attempt rebuilds runtime snapshots for
   that sending day and retries unacknowledged source-day aggregates
   with their same timestamps and UUIDs. A lost response can therefore
   leave a gap in `yardr_daily` if the server did not accept it, while
   duplicate source-day events are deduplicable.
4. On a 2xx with no `quota_limited` response entries, advances
   `last_day_sent` through the latest source day included, even when a
   day had no count events. `last_attempt_day` was already persisted
   before the request. On an HTTP error or a quota-limited response, it
   advances no source-day cursor; the next attempt is on the next UTC
   day. Source days skipped by the seven-day cap are explicitly dropped
   and will not be retried.

The endpoint and the key are **compiled in, never configured**: the EU
batch endpoint above, and the same public `phc_` project key footplate
hard-codes for its feedback (note 15552). There is no telemetry setting,
environment variable or flag for either, deliberately: a yard that
could be pointed elsewhere by config could be pointed elsewhere by
anyone who can write its config, and a public write-only key needs no
protection a setting could give it. The project is footplate's EU
project, shared, for three reasons: it already exists under the
project's own data-processing terms with EU residency; the cohort is
friends of the project, countable on fingers, and a second project
would split one tiny dataset across two retentions and two deletion
duties; and `yardr_`-prefixed event names cannot collide with feedback
events.
The cost is real and named: yardr's events inherit the feedback
project's retention and membership, and leaving later means a new
project, a new compiled key, and the old data deleted there — a bead,
not a migration. The key is public by design (it can only write
events); the document prints no value, as the value belongs in the two
clients' code and nowhere else.

A yard whose daemon does not run sends nothing. Telemetry is the
daemon's, as backups are; a CLI-only home has no daily batch and
question 4 never claimed it did.

### 3.4. Consent: one switch, off by default, set one way

The switch is one yard setting, `telemetry` (`true`/`false`, default
`false`, in `KnownSettings` beside `hosted`), and nothing else is a
switch: no environment variable (a variable would let a session's
environment consent for a yard whose operator never did), no per-event
flag, no config file. It defaults to the `Alone` ask class, as every new
setting does, so the operator types it and no run ask may propose it;
and `SetSetting` already refuses an agent, so no agent can turn its own
yard's telemetry on. Three commands, in `internal/cli/telemetry.go`,
registered as `yardr telemetry`:

- **`yardr telemetry on` / `off`** are the only CLI commands that change
  the `telemetry` setting; the generic `yardr settings set telemetry`
  refuses the key and directs the operator here, so a raw setting write
  cannot skip consent bookkeeping. These commands print what changed,
  where batches go, and the install id, with the pseudonymous-id
  statement in 3.5. Their help describes the four questions and the
  id-only PostHog profile used for deletion. `on` mints the install id first
  when none exists (3.5), records the UTC time of this consent grant,
  and records `eligible_from` as the next UTC day. A source day is
  included in counts only when telemetry was enabled for that whole
  UTC day, so locally recorded activity from before consent is never
  included. Repeating `on` while already on leaves `eligible_from`
  alone; turning off and on again starts a new eligible day and drops
  any unsent days from the earlier consent period. `off` prevents new
  requests from starting on the next daemon pass, records the withdrawal
  time locally, and a request already submitted may still complete. The
  id is kept, so a yard that turns off
  and on again is the same yard in PostHog; forgetting the id is a
  separate, deliberate act (3.5).
- **`yardr telemetry show`** prints the install id (3.7) and the exact
  request the daemon would send if it made a pass now. It sends nothing
  and advances nothing: it runs the section 3.2 builder against the
  current store and state, showing the sending-day runtime snapshot and
  any eligible completed source days in the batch. A hosted yard with
  no eligible source counts and no running crew has no request to
  preview. Because crew state
  and the store can change before the daemon's next pass, this previews
  the request from current state, not a promise that a later request
  will have identical counts. If telemetry is off or a request was
  already attempted today, it prints the id and says no batch is
  available until telemetry is enabled or the next UTC day. It never
  previews or sends pre-consent activity.
- **`yardr settings get telemetry`** keeps working, as for every
  setting; reading the state does not change consent.

**`yardr init` asks once.** The interactive onboarding (the `huh` form
in `internal/cli/init.go`) gains one confirm, after the yard exists,
defaulting to **No**: what telemetry is, the four questions, where it
goes, that PostHog receives a pseudonymous id and keeps an id-only
profile to support deletion, that `yardr telemetry show` previews the
exact allow-listed request from current state, and that `off` prevents
new requests from starting.
Answering Yes goes through the same consent activation as `yardr
telemetry on`, including minting the id and recording `eligible_from`;
answering No writes the off setting. A completed onboarding therefore
never asks again — a resumed onboarding finds the setting set and skips
the question. A non-interactive init
(`yardr init --harness <kind>`, scripts, the hosted provisioner) never
asks and leaves it off, printing the command that turns it on where a
terminal is available. Hosted-yard customers have no terminal, so
turning it on for them requires the browser action described in open
question 2; until that ships, hosted yards remain off. A yard from
before telemetry has no setting, which reads as off; its operator meets
the question only if a later interactive onboarding resumes, which is
the one other place the ask may appear. Asking once, at the
moment a person is already answering questions about their new yard,
beats asking at first daemon start (no person may be watching), in
`doctor` (a fault list is not a consent form), or on upgrade (nobody
reads upgrade output as a question). Losing: a prompt in every command
until answered — refused, because a consent that nags is not freely
given either.

### 3.5. Identity and data: pseudonymous, deletable, kept honestly

The install id is a random UUID (minted from
`crypto/rand`, never derived from the machine, the home's path, the
operator's name, or any yard content — a derived id could be recomputed
by anyone who knows the yard, and this one must be guessable by no
one). It is minted by `telemetry on` (`show` does not mint), and kept
with its bookkeeping in one file, `<home>/telemetry.json` (mode 0600,
beside the yard's other state files as `otel-export.json` is kept): the
id, the UTC date it was first minted, the timestamps of consent grants and
withdrawals, `eligible_from` (the first full UTC day after the latest
`on`), `last_attempt_day` (so a pass makes at most one POST attempt per
UTC day), and `last_day_sent` (the source-day cursor, advanced only
after a 2xx). Keeping it in a file,
not the store, means a store backup does not resurrect a
forgotten id into a restored yard, and deleting the file is a complete
local forget. The id never changes while the file stands — across
upgrades, restores of the store, and off/on cycles — because question 4
is meaningless if a yard becomes a new person at every reinstall.

**Pseudonymous, not anonymous — said wherever the id is shown.**
PostHog's `distinct_id` groups every event of one yard across time and
creates a person profile whose only supplied identity is that same
random id; no name, email or `$set` properties are sent. PostHog may
maintain profile timestamps. Joined with version, OS
and the day pattern, and with the tiny friends cohort, that can single
out a yard even though no name travels with it.
mckean's decision makes that a feature with a price: the user can paste
the id into a conversation and mckean can find their data when
reviewing. `show`, `on`, and the init question all carry the same
sentence: *this id is pseudonymous, not anonymous: anyone you give it
to can find this yard's events, and giving it is your act.* PostHog
receives the id as `distinct_id`; yardr does not send it to another
service, put it in event properties, or show it in the web view, mail,
`doctor` or `status` output. The project holds the id and its events,
but no direct name or account mapping unless a user shares the id.

**Deletion, by id.** A user asks the project to delete their data by
sending their install id (the conversation the id exists for, or any
channel they already use with the project). The People UI can find the
id-only profile by install id; the project uses PostHog's
`POST /api/projects/:project_id/persons/bulk_delete/` endpoint with
`distinct_ids: [<install id>]` and `delete_events: true`; the request
requires a project member's personal API key with person-write access,
and PostHog processes the deletion asynchronously. The profile carries
no identifying property beyond the install id. Two honest limits
travel with that promise.
Deletion removes what PostHog holds; it cannot recall an aggregate a
person already read, and while a deletion is in flight the id must not
be reused — so a user who asked for deletion and turns telemetry on
again first forgets the old id locally (turn off, delete
`<home>/telemetry.json`, turn on: a new id is minted, and the new yard
of data is unlinkable to the deleted one by anything yardr sends).
`show` prints this as its last line, with the id, so the route out is
written on the same screen as the thing it deletes. How long the
project takes to honour a deletion request, and who may make one, is
the project's to publish in the user doc (T3, section 6); this design
fixes only that the id is sufficient and that no second key is needed.

**What GDPR asks, stated without legal advice.** The [official GDPR text](https://eur-lex.europa.eu/eli/reg/2016/679/oj)
defines personal data as information relating to an identifiable person.
The random install id and linked events are personal data if they can
identify a person indirectly (Articles 4(1) and Recital 26); using an id without a
name is pseudonymisation, not anonymisation (Article 4(5)). The project
decides the purpose and means and is the controller; PostHog EU is its
processor under the DPA (Article 28). The proposed basis is consent
(Article 6(1)(a)): it is off by default, is a separate plain-language
choice, can be withdrawn as easily as it was given (Article 7), and the
yard records each grant and withdrawal time locally so the operator can
show when it was enabled. Turning it off prevents new requests from
starting; an already submitted request cannot be recalled. The event
allow-list and id-only person profile implement purpose limitation and
data minimisation (Article 5(1)(b)–(c)); retention must also be
limited (Article 5(1)(e)), which is the open question 1. T2 must verify
IP capture is disabled for this existing project before the first send;
the endpoint necessarily sees the connecting address, and the design
does not claim otherwise. The project provides access and portability
(Articles 15 and 20) by exporting events for the id, and erasure
(Article 17) by the API deletion above. These responsibilities belong
to the controller, not to the yard's local switch. Nothing here is
legal advice, and T3's user doc must not read as any.

### 3.6. Hosted yards: what the service already knows is not sent twice

Section 2 lists what the hosting service already sees. The design's
rule is one line: **questions 1 and 4 are answered for hosted yards by
the service, so the client sends no version or daily-activity event for
them.** Self-run yards' `yardr_daily` events are therefore the only
source for those PostHog questions. For hosted yards, the server-side
activity measure is a routed request on day 1, 7 or 30, not proof that
the yard's daemon ran all day. The client reads `hosted` to decide
whether to build `yardr_daily`; the provisioner sets it to `true` on
the yards it makes.

What hosted yards uniquely add is what the service cannot see:
questions 2 and 3, inside the yard. Their `yardr_harness_day`,
`yardr_crew_day` and `yardr_failure_day` events are counted with
everyone else's, with `hosted` on each event for a breakdown. A hosted
yard with no eligible counts and no running crew sends no batch. The
provisioner never sets `telemetry`. Hosted customers have no terminal;
if option (a) in open question 2 is chosen, T3 adds browser controls to
set this same global switch and to show the install id and payload after
the customer opts in. That control writes the setting through the same
`ActChangeSetting` gate (`may`) as the CLI, as the yard's operator in
their own web session: it adds no caller that may change the setting —
no agent in a pane, linked yard, hook or edge request may turn it on —
and the provisioner and the edge never call it. Until those controls exist, hosted yards stay off.
No event carries the account, handle, or yard name the service knows —
the client has no property for them (3.1).
The install id remains pseudonymous in PostHog, but the hosting operator
already has an account-to-yard map and operates the yard's machine; the
design does not claim that operator is technically unable to associate
the yard's local id file with its account. It promises only that no
account fields are sent to PostHog. Whether hosted yards should send at all
is open question 2: the recommendation is that they may, on their
customer's switch alone, because questions 2 and 3 have no server-side
source; the alternative — the service asking its customers separately
and the client never sending when `hosted` is true — is stated there
with its cost.

### 3.7. What a person sees

`yardr telemetry show`, on a yard that has not attempted today's batch:

```text
install id: 3f6d1c2e-9a4b-4e7a-b5c1-2d8f0a6e4b90
This id is pseudonymous, not anonymous: anyone you give it to can
find this yard's events, and giving it is your act.

telemetry: on (since 2026-10-26)
next request: sending day 2026-10-28 (UTC); source day 2026-10-27
{
  "api_key": "phc_…",
  "batch": [
    {"event": "yardr_daily", "distinct_id": "3f6d…",
     "timestamp": "2026-10-28T00:00:00Z",
     "uuid": "0e3c8b2a-1432-4a51-9f39-1bc7a1b1d001",
     "properties": {"$process_person_profile": true,
       "version": "v0.9.1", "os": "darwin",
       "telemetry_age_days": 2}},
    {"event": "yardr_crew_day", "distinct_id": "3f6d…",
     "timestamp": "2026-10-28T00:00:00Z",
     "uuid": "451d5fc1-4a12-4c20-9d7b-1f9df88ca004",
     "properties": {"$process_person_profile": true,
       "kind": "claude", "model": "default",
       "running_crew_sessions": 2, "hosted": false}},
    {"event": "yardr_harness_day", "distinct_id": "3f6d…",
     "timestamp": "2026-10-27T12:00:00Z",
     "uuid": "a98276f4-0f5a-47a8-b98a-8ef17b30f003",
     "properties": {"$process_person_profile": true,
       "kind": "claude", "model": "default",
       "sessions_claimed": 14, "hosted": false}},
    {"event": "yardr_failure_day", "distinct_id": "3f6d…",
     "timestamp": "2026-10-27T12:00:00Z",
     "uuid": "f481958d-ea90-4a3c-8ed7-0a1ff35af004",
     "properties": {"$process_person_profile": true,
       "kind": "held", "count": 2, "hosted": false}}
  ]
}

To have this yard's data deleted, send the project this install id.
```

(The key is shown whole by the real command on the user's own machine;
it is shortened here as this document is not a client. The `…` in the
ids is this document's, for the same reason.) Off, the first lines read
`telemetry: off — nothing is sent`, then `no batch available`; no
pre-consent source day is previewed as a future send. `--json` prints
the same object the text renders, through `a.out`, as every command
does.

## 4. Left out

- **The implementation.** This bead is the design; section 6 files it
  as beads. No code, flag, setting or state file exists until they land.
- **Any dashboard in PostHog.** Insights, cohorts and boards are made
  in PostHog when the questions are first read, by whoever reads them;
  nothing in the clients depends on them.
- **The footplate side.** Footplate has no telemetry; its feedback form
  (footplate-ps5h) shares the project and nothing else. Footplate does
  not display, set, or carry the yard's switch or id.
- **Per-category or per-event choices.** Decided against by mckean:
  one switch. A future category needs a new decision, not a flag.
- **Finer or richer signals.** No durations, token counts, stage
  names, depot counts, bead counts, or success counts: the four
  questions do not ask, and each addition would re-open the never-list.
- **Sending from anywhere but the daemon**, backfill past 7 days, and
  real-time sending: a daily batch answers all four questions, and
  everything fresher buys nothing but surface.
- **The OTel exporter as a channel.** It stays what it is: a person's
  own collector, their own choice, sending names this design forbids.
  Telemetry neither reuses nor replaces it.
- **Automatic deletion or expiry inside the client.** The yard cannot
  delete what PostHog holds; it can only stop sending and forget its
  id. Expiry in PostHog is open question 1.

## 5. Open questions

Both are mckean's to take; each keeps its options, its costs, and the
recommendation this design would build if he does not.

1. **How long does PostHog keep the raw events?** PostHog keeps events
   until the project deletes them or a retention is set; the yard
   itself keeps its own log 90 days, but the telemetry copy is the
   project's, and GDPR's storage-limitation principle wants a period
   named in advance. (a) *12 months, then raw events deleted* (project
   setting or scheduled deletion), with any longer record kept as
   aggregates made before deletion: a year covers the 30-day cohorts
   many times over and every version's working life so far. (b) *Keep
   indefinitely while the cohort is friends of the project*, revisited
   at public release: the least machinery now, and a promise that gets
   harder to keep the longer it runs. (c) *90 days, matching the
   yard's own retention*: the tightest, and the 30-day question still
   answers, but no version can be followed across a season. I would
   take (a): it is the longest period any of the four questions can
   use, and it is a date the user doc (T3) can print.
2. **Do hosted yards send at all?** Section 3.6 omits questions 1 and
   4 because the service has those records. Hosted customers have no
   terminal, and the non-interactive provisioner leaves telemetry off.
   (a) *Send questions 2 and 3 with a hosted browser control*: T3 adds
   an opt-in control and a view of the install id and next payload for
   that same global switch. The service learns the inside view only
   after its customer opts in; this costs a hosted UI path. (b) *Never
   send when `hosted` is true*: no hosted UI path, no client events, and
   the service uses only its own records for hosted yards — cleaner
   custody, at the cost of losing harness/model and failure counts for
   the yards the project operates. I would take (a) if the UI control
   ships with T3; until then hosted yards remain off. Both choices keep
   one switch and omit the server-side questions from the client.

## 6. The beads it would become

In the order they would land, each in the yardr depot, each passing
`sh .yardr/test`: T1 builds the switch and local preview, T3 adds the
onboarding consent question and user documentation (plus hosted browser
controls if open question 2(a) is chosen), then T2 adds the network
call. T2 depends on both T1 and T3, so no network sender can
land before the notice and init prompt. T1's `on` command and its help
state the four questions and destination; an enabled setting alone
sends nothing until T2 lands.

**T1. The switch, the id, the builder, and `show` (no sending).** Goal:
the `telemetry` setting in `KnownSettings` (default `false`, `Alone`);
`<home>/telemetry.json` minted and kept as 3.5 says; the pure day
builder of 3.1/3.2 over the store; `yardr telemetry on|off|show` in
`internal/cli/telemetry.go`, registered in `root.go`, printing per
3.7. Nothing is POSTed; the daemon is untouched. Where to look:
`internal/store/settings.go` (`hosted` for the shape), `internal/store/events.go`
(`EventsOfKind`), `internal/store/routing.go` (`Claim`'s kind/model),
`internal/config` (home paths, `OTelState` for the state-file shape),
`internal/cli/init.go`'s `settingsCmd` for command style. Done when:
on a throwaway store, `on` mints an id that `show` prints beside the
current request, built to 3.1 exactly (fixture yard with
sentinel-named beads, depots, groups and a sentence-shaped model: the
batch holds none of them, an unlisted short model value is also `other`,
a listed offer stays fixed, an empty model is `default`, and an untagged
build is `development`). A day before `eligible_from` is never included;
`off` then `on` keeps the id but advances `eligible_from`; direct
`settings set telemetry` is refused with a hint to use `on` or `off`;
deleting the state file and turning on mints a new one. A self-run day
with no claims, failures or running crew yields `yardr_daily` alone; a
hosted yard with no such counts yields no request. Rebuilding source
days yields identical timestamps and UUIDs; `telemetry_age_days` can be
filtered at 1, 7 and 30, with no backfill for a missed daily send;
`sh .yardr/test` passes.

**T2. The daemon sends the daily batch.** Goal: the `telemetry` pass
step of 3.3 in `internal/dispatch`, the compiled EU endpoint and
`phc_` key, 10-second timeout, at most one POST attempt per UTC day,
one warning per failed attempt with no payload or ID, at most 7 eligible
source days per batch, source cursor advanced only on a 2xx without
quota-limited entries, off discarding unsent days, retries with stable
source-event UUIDs and timestamps; the
shared project's IP capture is verified disabled and its timezone is
set to UTC before the first send, both written in the runbook note this
bead leaves. Depends on T1 and T3.
Where to look: `internal/dispatch/backups.go` (the step, the outage,
the per-pass settings read), `internal/otel` (hand-written HTTP JSON,
no SDK). Done when: against a stand-in HTTP listener (never the real
project), an enabled throwaway yard sends at most one well-formed batch
per UTC day, a failed attempt waits until the next day to retry and logs
one warning with no request data, response body or ID. Turning off
mid-day starts no later request;
a 3-day eligible gap backfills 3 days in one batch and a 10-day gap
includes 7 with the earlier range logged. Hosted yards omit
`yardr_daily` and send only crew or source-day count events. An off/on cycle skips all
days before the new `eligible_from`; a response-lost retry preserves
source-event UUIDs and timestamps while an included self-run daily
snapshot describes the retry day. A quota-limited 2xx advances no
source cursor. With no
state changes between preview and send, the
stand-in receives the request `show` printed; `sh .yardr/test` passes.

**T3. Init asks once, and the user doc says what the switch means.**
Goal: the onboarding confirm of 3.4 (interactive only, default No,
written either way, skipped when set, never in non-interactive init);
if open question 2(a) is chosen, a hosted browser control for the same
on/off switch, install id and exact payload preview;
`docs/telemetry.md` for users — the four questions, hosted-yard
handling, the event list in plain words, the pseudonymous sentence, how
to see a batch, how to stop and when pre-consent days become eligible,
how to be deleted by id
through the project API, and the retention open question 1 settled —
linked from `docs/configuration.md`'s settings table with
the `telemetry` row. Where to look: `internal/cli/init.go`
(`onboardForm`, the `huh` confirms), `docs/otel.md` for a user doc
that says what leaves the machine. Done when: a scripted onboarding
answers No and is never asked again, one answers Yes and the setting
stands `true`, `yardr init --harness claude` asks nothing and leaves
it off, and the doc's event list matches T1's builder property for
property (a test or a checklist in the bead, not prose agreement). If
option 2(a) is chosen, the hosted browser control opts in only on an
explicit customer action and shows the same id and preview as the CLI;
`sh .yardr/test` passes. Depends on T1; T2's enablement in the wild
waits for this bead's words.
