# Tokens per session, per bead, group and label

Design yardr-qbqq, proposed 2026-10-21. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead.

## 1. The question

On Telegram on 21 Oct, between 22:08 and 22:12, mckean asked whether the
yard can read token usage, and then whether per-bead cost should be an
enterprise add-on or part of yardr itself. The yardmaster recommended
building it into yardr, and mckean answered "alright design we go!". The
bead asks for a way to count the tokens each session of the yard uses,
by bead, group, estimate label and originator, so that the operator, and
later the routes, can see which kinds of beads cost the most against the
subscription windows. It counts tokens, not dollars: the yard runs on
subscriptions (Claude Max, Codex Plus, Muse), and tokens are what fill
the quota windows that `yardr limits` reads.

Some terms used below. A *session* is one agent working one bead in one
stage (`yardr docs glossary`). The *harness* is the agent program a
session runs (Claude Code, Codex, Muse), and its *kind* is the name
yardr gives it (`claude`, `codex`, `muse`). The *transcript* is the file
in which a harness records a session as JSON lines. Each line is one
JSON value, so "a line" below means one record. *Cache read* tokens are
prompt tokens the provider served from its prompt cache. *Cache write*
tokens are prompt tokens it stored in that cache. Providers count them
apart from fresh input.

## 2. What exists today

**Reading a session's own transcript.** `internal/agents/harness.go`
defines two sources a harness file may declare:

- `ModelSource` (`model:` in `harnesses/*.yaml`) says where the
  transcript is: a `Glob` under the harness's home directory (`Home`, or
  the directory `HomeEnv` names), with `{session}` standing for the
  session id that herdr reports for the pane (`agent get`:
  `agent_session`). It also says which line holds the model (`Type`,
  `TypeField`, `Field`). `internal/runner/model.go` finds the file
  (`Herdr.sessionKind`, `Herdr.agentHome`, `firstModel`), and
  `internal/dispatch/model.go` records the result on the session's
  handle as `model_reported`, during the first 15 minutes of the session
  (`modelWindow`).
- `ContextSource` (`context:`, Claude only) reads the *last* line of
  `Type` and adds up the fields it lists (`Tokens`, by default
  `DefaultContextTokens`: `input_tokens`, `cache_read_input_tokens` and
  `cache_creation_input_tokens`) of the object at `Usage`.
  `internal/runner/context.go` (`Herdr.Context`, `lastUsage`) reads it,
  and `internal/dispatch/crewcontext.go` (`crewContext`) turns it into
  the crew member's context percentage and handoff nudge.
  `Harness.validate` refuses a `context:` that has no `model:`.

Only `claude.yaml`, `codex.yaml` and `muse.yaml` have a `model:`; only
`claude.yaml` has a `context:`. Grok, Kimi, Kiro, OpenCode and Pi have
neither.

**The session record and its handle.** `store.Session`
(`internal/store`) holds the bead, group, stage, kind, model, state,
start and end times, and a free-form `Handle` map. The dispatcher keeps
facts about the running agent on that map (`model_asked`,
`model_reported`, `worktree`, `pane`, `shell_pid`), written with
`SetHandle`. `yardr session list --all --json` shows the map. When a
session ends, `Dispatcher.teardown` (`internal/dispatch/dispatch.go`)
closes its herdr tab (`Herdr.Stop`: `tab close`) and calls `MarkStopped`.
Once the bead is closed, `Dispatcher.cleanup` removes the worktree and
calls `MarkCleaned`. Neither step deletes the session record. `Prune`
removes ended sessions together with events, after
`YARDR_EVENT_RETENTION` (90 days by default, `docs/configuration.md`,
"Retention"). The yard does not keep the harness's own session id
anywhere: `sessionKind` (`internal/runner/model.go`) asks herdr for it
each time it is needed and hands back only what it read with it.
`Dispatcher.model` (`internal/dispatch/model.go`) stores
`model_reported` and nothing else, and only once a model was read. After
`tab close` herdr no longer knows the agent. A group with `keep: true`
in its config is the exception in `teardown`: `Stop` is skipped, the
pane and its agent stay alive for inspection, and the session is still
marked stopped.

Each of these store calls is one bbolt transaction (`Store.write`).
`SetHandle` and `LogDispatch` are two separate ones. `changeSession`
(`internal/store/routing.go`) hands its function a `wtx`, whose `event`
method appends an event inside the same transaction, so one store call
can change a session and log an event together, or do neither.

**Crew sessions.** `store.CrewSession` (`internal/store/crew.go`) has
its own `Handle`, plus the context reading (`ContextPct`,
`ContextWindow`, `ContextAt`). A crew session ends in two ways. On a
rotation, a restart or a disable, `Dispatcher.stopCrew`
(`internal/dispatch/crew.go`) closes it and calls `CrewStoppedFor`,
which sets `Handle` to nil and logs `crew_stopped` in the same
transaction (`internal/store/crewsession.go`). When the agent is found
gone, `CrewGoneFor` moves the handle to the end of `Left` (what the
daemon closes later) and logs the same event. So once a crew session
has ended, its handle is no longer where the running session's was.

**The event log and the route report.** `LogDispatch`
(`internal/store/events.go`) appends one event of a kind listed in
`DispatchKinds`. `yardr route report` (`internal/cli/routereport.go`,
`summarizeRouteEvents`) reads `claimed` events. Each of those carries
`session`, `group`, `stage` and `kind`, and from them the report builds
one row per GROUP / ORIGINATOR / LABEL with the columns BEADS, SENT
BACK, REVIEW FIXED, SEC FIXED, FAILED and MEDIAN BUILD, plus a BY
HARNESS table (SESSIONS, HOURS, MEDIAN, STAGES). It already maps
sessions to claims (`sessionOf`). It counts only closed beads, and only
events still retained.

**There is no `yardr session show`.** The bead names one, but `yardr
session` has only `list` and `snooze`. `yardr bead show` prints the
bead's owner session and its notes, not its sessions.

**OpenTelemetry.** `docs/otel.md`, "Claude Code": when the operator sets
`CLAUDE_CODE_ENABLE_TELEMETRY` and an OTLP endpoint, Claude Code sends
token and cost metrics, and the `yardr.bead` / `yardr.stage` resource
attributes yardr puts in the pane's environment are attached to them.
Codex is configured in `~/.codex/config.toml`. Neither setup was run
against a collector, and Muse has no section there.

**Limits.** `internal/limits`: `yardr limits` runs the operator's
command (here `aiquokka <provider> --json`, declared in
`~/.yardr/sources/aiquokka.yaml`) and keeps `limits.Report` with its
`Window`s: `used_percent`, `target_percent`, `resets_at`, and optional
`used` and `limit` fields.

**Gas City, for comparison.** `gc costs` (`cmd/gc/cmd_costs.go` on
gascity's origin/main) reads `.gc/usage.jsonl`, where
`internal/worker/invocation_telemetry.go` writes one `usage.Fact` per
model call. It finds those calls by tailing each transcript after every
prompt operation, keeping a cursor per session. The extractor code is
specific to each transcript family (Claude, OpenCode, Codex), and
`internal/pricing` prices each call at list price. It is a per-call
ledger fed by code written for each harness. What is proposed here is
one total per session, declared in each harness's file.

### What the transcripts say, checked on this machine on 21 Oct 2026

**Claude Code 2.1.296.** File: `~/.claude/projects/<dir>/<id>.jsonl`.

- Each `assistant` line carries `message.usage`. Claude Code writes one
  line per *content block*, and every line of a message repeats the
  whole message's usage. In the builder session of yardr-mnc8
  (`4b176be6…`), 223 assistant lines belonged to 121 messages. Adding up
  every line gives 159,912 output tokens; counting each `message.id`
  once gives 74,962. **Adding up every line counts most turns two or
  three times.**
- The last line of a session that ended is `{"type":"cost-state", …}`.
  Its `modelUsage` holds Claude Code's own totals for each model:
  `inputTokens`, `outputTokens`, `cacheReadInputTokens`,
  `cacheCreationInputTokens`, `thinkingTokens`, `costUSD`. Of the 80
  most recent transcripts of yard worktrees, 78 had exactly one such
  line. The two without one were the sessions still running (this one,
  and yardr-tx11's).
- For yardr-mnc8, `cost-state` reads: Opus 752 input, 74,978 output,
  23,562,912 cache read, 247,751 cache write, plus Haiku 960 input and
  23 output. The Haiku calls are Claude Code's side calls, which have no
  transcript line.
- In yardr-qqnv's builder session (`05981b74…`, Sonnet), the
  per-message sums of the main file plus its
  `<id>/subagents/agent-*.jsonl` files match `cost-state` *exactly* on
  input (434), cache read (82,200,424) and cache write (738,275). Output
  is 299,587 against 376,967. The gap is close to the gap in
  `thinkingTokens` (241,438 in `cost-state` against 162,537 in the main file's
  lines), so the lines miss thinking that `cost-state` counts.
  `cost-state` also counts 54,077 Haiku input tokens. So `cost-state` is
  the complete count. The lines, read once per message and with the
  subagents included, are a lower bound.

**Codex (codex-cli 0.160.1).** File:
`~/.codex/sessions/<y>/<m>/<d>/rollout-<time>-<id>.jsonl`.

- `event_msg` lines whose `payload.type` is `token_count` carry
  `payload.info.total_token_usage`, a running total for the session,
  and `last_token_usage` for the latest call. In the review session of
  yardr-mnc8 (`01a16016…`, 27 calls), adding up the 27
  `last_token_usage` values gives exactly the last
  `total_token_usage`: 2,879,571 input, of which 2,709,248 cached, and
  25,036 output. The last line is therefore the session's total, at any
  moment, with no end-of-session line needed.
- `input_tokens` *includes* `cached_input_tokens`, and `total_tokens`
  is input plus output (2,879,571 + 25,036 = 2,904,607).
  `reasoning_output_tokens` is part of `output_tokens`.
  `cache_write_input_tokens` was 0 on every line.
- The same lines carry `rate_limits` (`used_percent` of the 300-minute
  and 10,080-minute windows, `plan_type: plus`), the same figures
  aiquokka reports.

**Muse Code 1.4.11. It does record usage.** File:
`~/.local/share/muse/sessions/<y>/<m>/<d>/<id>/session.jsonl`.

- Every model call writes a `runtime.session` line whose
  `payload.event.kind` is `goal_usage_attribution`. Its
  `payload.event.record.quantity` holds `input_tokens`,
  `output_tokens`, `cached_tokens` and `reasoning_tokens`, and
  `owner.requester_kind` says who made the call (`main`, or `reminder`
  for a side call). Records of `usage_family` `tool` or `reminder`
  carry zero tokens.
- Calls of the main agent also write a `model_completed` line, with
  `usage` (`input_tokens`, `output_tokens`, `cached_tokens`,
  `cache_read_tokens`, `cache_write_tokens`). Side calls do not.
- A side call runs as a child under `<id>/subagent/<child>/`, and its
  usage appears twice: in the child's file, and in the parent's file as
  a `goal_usage_attribution` record with another `usage_id`. Both
  records carry the same token counts (72,431 and 10,299 input in
  `01a1600a…`). So **the parent's `goal_usage_attribution` lines alone
  are the session's whole count**, and the children's files must not be
  added to them.
- No line holds a running total. `session.end` carries only
  `exit_reason` and `uptime_ms`. Muse's count is the sum of the lines.
- `cached_tokens` is never more than `input_tokens`. yardr-58xw's
  builder session: 29,069,759 input, of which 28,600,326 cached. The
  cached tokens are read here as part of the input, as Codex's are; no
  Muse documentation was found that says so.
- `reasoning_tokens` is part of `output_tokens`, as Codex's
  `reasoning_output_tokens` is. Over every Muse file on this machine
  (1,358 sessions, 189,071 `quantity` records), 65,841 records have
  reasoning tokens, and in none of them is `reasoning_tokens` equal to or
  above `output_tokens`; the highest share is 98.6 per cent. Were
  reasoning counted apart, a call that thought at length and answered
  with a short tool call would show more reasoning than output, and
  there are tens of thousands of such calls (`finish_reason:
  tool_calls`). No Muse documentation was found that says so.
- herdr 0.9.0 reports no `agent_session` for Muse panes (`herdr agent
  list`, checked today), so the `{session}` glob finds nothing for them.
  That is why `model_reported` is empty for every Muse session. The
  first metadata line (`runtime.session.metadata`) does carry
  `payload.record.workspace_root`, the worktree. For yardr-58xw, the
  two Muse files with that worktree started 4 s and 3 s after the
  `claimed` events of the yard sessions they belong to (new at 18:46:13
  and 18:46:17; review-security at 19:36:32 and 19:36:35).

**One bead, end to end: yardr-58xw.** It went through three sessions,
in the stages `new`, `review` and `review-security`. Fresh input below
means input without cache reads.

| stage | group | kind | fresh input | cache read | output |
|---|---|---|---|---|---|
| new | muse-builders | muse | 469,433 | 28,600,326 | 122,644 |
| review | yardr-reviewers | codex | 213,352 | 2,603,264 | 21,396 |
| review-security | muse-security | muse | 205,114 | 1,752,134 | 26,241 |

Cache reads are 88 to 98 per cent of every session's tokens. Any single
number that adds them in is mostly cache reads.

## 3. The proposal

The count is read **once per session, after the yard has stopped it**,
from the harness's own transcript. Each harness file declares where its
count is, in a `usage:` key next to `context:`, so no Go code is
written for any one harness. The harness's session id is recorded on the
handle while the session runs, because herdr forgets it at teardown. The
reading is kept on the session's handle and logged as one
`session_usage` event, both in one store transaction; for a crew
session, the `crew_usage` event is the record. The route report reads
these events next to its `claimed` events. Nothing reads the
transcripts for tokens during the session.

### 3.1 Where and when the count is read (question 1)

- **The id, while the session runs.** A new step,
  `Dispatcher.agentSession`, runs on every pass for each running session
  whose handle has no `agent_session` yet. It asks the runner for the
  id alone (`Herdr.SessionID`: what `sessionKind` reads, without the
  model) and writes it with `SetHandle` as soon as herdr gives one. It
  does not wait for a model line and does not depend on what the model
  check finds, so a session that dies before its first answer, or whose
  harness names no model, still has its id. Like the model check, it
  stops asking `modelWindow` (15 minutes) after the start: herdr knows a
  Claude or Codex id within seconds of the agent starting, and a kind it
  gives none for (Muse today) is not asked about on every pass for ever.
  `teardown` asks once more just before `Stop` when the handle still has
  none. Because the id is on the stored handle, a daemon that replaces
  itself in between finds it there.
- **The count, after the session is stopped.** After `MarkStopped`,
  the session waits with no `usage` on its handle. On each later pass
  (`d.Interval` ticks and committed writes), `Dispatcher.usage` reads
  every stopped session that has an `agent_session` and no `usage`. A
  process that has exited has written its last line, so one read is
  enough. Claude writes `cost-state` as it exits, a moment after the tab
  closes, so a Claude session whose file does not have that line yet is
  read again on the next pass. The result is written by one store call,
  `RecordUsage` (3.2), which sets `usage` on the handle and logs the
  event in a single transaction.
- **When the count is not there.** A source declares its readings in
  order (3.3). The first is the harness's own total. If it is still
  missing 10 minutes after `MarkStopped` (a Claude process that was
  killed writes no `cost-state`), the next declared reading is used,
  and the result is kept with `partial: true`. If the file is gone, or
  the kind has no `usage:`, the result is `{missing: "<why>"}`: `no
  session id`, `no file`, `no usage line`, or `kind declares no usage`.
  Every session ends with exactly one result, so the report can say how
  many it could not count.
- **Rotation and compaction.** Claude Code deletes transcripts after
  `cleanupPeriodDays` (30 by default). A count read within minutes of
  the end never sees that happen. Compaction appends to the same file;
  `cost-state` and Codex's `total_token_usage` are totals kept by the
  running process, so compaction does not reset them. Muse's sum covers
  every call recorded in its file. If a harness started a new file for
  the same session, only the file named by the id would be read, and
  the count would be short. That was not seen in the files checked, and
  the document only names it.
- **Long sessions.** A session that runs for hours over many turns is
  still read once, at the end. Its file is a few MB (the yardr-mnc8
  builder file was 2.2 MB), and one read at the end is cheaper than
  following the file with a cursor as Gas City does.
- **Crew sessions.** `agentSession` records the id on the crew handle
  too (`SetCrewHandleFor`), in the first 15 minutes of the crew session.
  Since `CrewStoppedFor` clears the handle and `CrewGoneFor` moves it to
  `Left`, both are extended to keep what the count needs in the same
  transaction that ends the session: one entry `{agent_session, kind,
  started_at, stopped_at}` appended to a new list on the crew session,
  `Uncounted`. `Dispatcher.usage` reads each entry, and
  `RecordCrewUsage` removes it and logs a `crew_usage` event with
  `member` and `user`, again in one transaction. The event is the
  durable record of a crew session's count; nothing is written to a
  handle. It is not attached to any bead, even when the crew member
  shaped one: crew members are tied to no bead (glossary). A crew
  session runs until it is rotated, so its count arrives at rotation.
  The live context percentage that exists today stays as it is.
- **Sessions with nothing to read.** Manual sessions and exec runners
  have no herdr agent, and so no id and no count. They get `missing: no
  session id`.
- **A `keep: true` group** leaves its agent running for inspection.
  The amount this design counts is the **yard-stop snapshot**: what the
  session had spent when the yard marked it stopped. The work the yard
  asked of the session ended there; whatever a person does in a kept
  pane afterwards is not the bead's. The handle gets `kept: true` from
  `teardown`, and the reading carries it as well. Codex's last
  `token_count` and Muse's sum are complete for the work up to that
  moment, so their reading is not marked short. Claude writes no
  `cost-state` until its process exits, so a kept Claude session falls
  back after the 10 minutes to its message lines and reads `partial`.
  What `kept` changes in the report is nothing more than the `--json`
  field; `partial` is what marks a count as short.

### 3.2 What is stored, and where (question 2)

The reading has the same shape wherever it is kept:

```json
{"input": 469433, "output": 122644, "cache_read": 28600326, "cache_write": 0,
 "partial": false, "read_at": "2026-10-21T19:26:02Z"}
```

`input` never includes cache reads. Each harness's figures are
converted to these four as part of the reading (3.3), so a Codex row and
a Claude row can be added together. `partial` and `kept` (3.1) appear
only when true. A session with nothing to count has `{"missing": "<why>",
"read_at": …}` in its place.

**A bead's session** keeps it in two places, written by one new store
call, `RecordUsage(ctx, sessionID, usage)`. It is the daemon's alone
(`daemonOnly`, as `SetHandle` is) and runs inside `changeSession`, so
the two writes are one transaction:

1. **On the session handle**, under the key `usage`, next to the
   `agent_session` recorded earlier. No migration: the handle is a free
   map, and `yardr session list --all --json` shows it.
2. **As an event**, `session_usage`, logged for the session's bead
   through the `wtx`'s `event` (one more entry in `DispatchKinds`), with
   data `{session, group, stage, kind, usage}`. The route report reads
   it with its `claimed` events.

If the daemon dies before the transaction commits, neither write is
there and the session is read again on the next pass. If it dies after,
both are there. `RecordUsage` does nothing when the handle already has a
`usage`, so a repeated call logs no second event.

**A crew session** keeps it in one place: the `crew_usage` event, with
no bead and data `{member, user, kind, agent_session, usage}`. The crew
handle is not used, because it is cleared or moved when the session
ends (section 2). Until the count is read, the session's entry waits in
`CrewSession.Uncounted`, written in the same transaction as
`crew_stopped` (3.1). `RecordCrewUsage(ctx, name, user, agentSession,
usage)` removes that entry and logs `crew_usage` in one transaction, so
an entry is either still waiting or counted, never lost and never
counted twice. `Uncounted` is a new field on a JSON-encoded record, and
needs no migration.

`MarkCleaned` deletes nothing, so the count outlives the removal of the
session's worktree. It is pruned with the session and its events, after
`YARDR_EVENT_RETENTION` (90 days by default). How long it should be kept
beyond that is open question 3.

### 3.3 How a harness file declares it (question 3)

`ContextSource` already does most of the work: it finds lines of a
type, follows a dotted path to an object, and adds up named fields from
it. `UsageSource` is the same thing generalised in four small ways:

1. **`type_field` may be a dotted path**, as `usage` already is. Codex
   and Muse keep the type of a usage line in a nested field
   (`payload.type`, `payload.event.kind`). For `ModelSource`'s existing
   one-word values this changes nothing.
2. **A `*` segment in the path** adds up every value of a map, so that
   Claude's `modelUsage.*` covers every model, Haiku side calls
   included.
3. **`read: last | sum`.** `last` (the default, and what
   `ContextSource` does) takes the last matching line. `sum` adds up
   every matching line. `key`, a path, counts only the last line for
   each value it finds, which is how Claude's repeated lines would be
   counted once.
4. **Fields by meaning.** `input`, `output`, `cache_read` and
   `cache_write` each name the field that holds that count.
   `cache_in_input: true` means the harness's input includes its cache
   reads, and they are subtracted from it. A field that is not named
   reads as 0.

`usage:` is a list of these readings, tried in order. The first that
finds a line is the result. An entry marked `partial: true` produces a
`partial` count. The extended `validate` requires a `model:` (as for
`context:`), at least one of the four fields, and a non-empty `key` when
`key` is given.

```yaml
# claude.yaml
usage:
  # Claude Code's own totals, written as it exits: every model, side
  # calls and thinking included.
  - type: cost-state
    usage: modelUsage.*
    input: inputTokens
    output: outputTokens
    cache_read: cacheReadInputTokens
    cache_write: cacheCreationInputTokens
  # A process that was killed writes no cost-state. Each message's lines
  # repeat its usage, so each message.id counts once. Lower than
  # cost-state: no side calls, and less thinking.
  - type: assistant
    usage: message.usage
    read: sum
    key: message.id
    partial: true
    input: input_tokens
    output: output_tokens
    cache_read: cache_read_input_tokens
    cache_write: cache_creation_input_tokens

# codex.yaml: the last token_count is the session's running total.
usage:
  - type_field: payload.type
    type: token_count
    usage: payload.info.total_token_usage
    input: input_tokens
    cache_in_input: true
    cache_read: cached_input_tokens
    cache_write: cache_write_input_tokens
    output: output_tokens

# muse.yaml: no total is written; every call has a record in the
# parent's file, side calls included. reasoning_tokens is inside
# output_tokens (never above it in 65,841 records), so it is not added.
usage:
  - type_field: payload.event.kind
    type: goal_usage_attribution
    usage: payload.event.record.quantity
    read: sum
    input: input_tokens
    cache_in_input: true
    cache_read: cached_tokens
    output: output_tokens
```

Claude's fallback reads the main file only. Subagent files are left out
(open question 4). Muse reports no cache writes in these records, and
`model_completed`'s `cache_write_tokens` was 0 in both Muse sessions
checked, so Muse has no `cache_write`. Grok, Kimi, Kiro, OpenCode and Pi
have no `model:` and therefore no `usage:`. Their sessions read
`missing: kind declares no usage`, and they gain a count by gaining
both keys in their files. No Go code changes for that.

**Finding Muse's file.** None of this reaches Muse while herdr gives no
id for its panes, and Muse ran 1,316 of the sessions in the route
report's window, more than any other kind. Muse has no `--session-id`
flag (`muse --help`). The proposal is one more optional key on
`ModelSource`:

```yaml
model:
  # …
  workspace: { type: runtime.session.metadata, field: payload.record.workspace_root }
```

A kind with `workspace` and no session id from herdr is found by the
glob with `{session}` read as `*`. The file chosen is the earliest one
whose `workspace` line names the session's worktree (`handle.worktree`)
and whose first line was written at or after the session's
`StartedAt`, within `modelWindow`. The file's id then goes on the handle
as `agent_session`, as for the other kinds, and the model check gains
Muse at the same time. Crew members share one working directory (the
operator's home), so a Muse crew session may match the wrong file.
`crew_usage` takes Muse only from herdr's id; until herdr gives one, a
Muse crew session reads `missing: no session id`. Whether to build this
key or wait for herdr is open question 2.

### 3.4 How it shows (question 4)

The headline number in this section is *new tokens*: fresh input, cache
writes and output added together, leaving out cache reads. The
yardr-58xw table shows why: cache reads would make up nearly all of any
total that included them. Which number leads is open question 1.

`yardr route report` gains two columns and one table:

```
GROUP            ORIGINATOR  LABEL         BEADS  SENT BACK  …  MEDIAN BUILD  TOKENS/BEAD  TOKENS  UNCOUNTED
muse-builders    mckean      difficulty:2  290    0.3        …  20m           640k         ≥181M   3
yardr-reviewers  mckean      difficulty:2  301    0.1        …  3m            235k         70.6M   0

BY HARNESS
KIND    SESSIONS  HOURS   MEDIAN  TOKENS  UNCOUNTED  STAGES
codex   1292      5d 4h   3m      304M    12         critique, review
muse    1316      14d 19h 11m     780M    40         draft, new, review-security, shape

CREW
MEMBER       SESSIONS  TOKENS  UNCOUNTED
yardmaster   4         3.1M    0
```

(The numbers are made up to show the layout; nothing has counted them
yet.)

- **TOKENS/BEAD** is the median, over the cell's beads, of the new
  tokens the group's sessions spent on each bead. Only beads all of
  whose sessions in that group were counted take part, so a bead with a
  missing session does not drag the median down. A cell with no such
  bead shows `-`, as MEDIAN BUILD does. **TOKENS** is the total of every
  counted session in the cell.
- **UNCOUNTED** is the number of the cell's sessions whose result is
  `missing`. Whenever a cell holds a `missing` or a `partial` result,
  TOKENS prints `≥` in front: the total is then a lower bound, and the
  mark says so on the row where it applies, not only in BY HARNESS.
- **BY HARNESS** gains TOKENS (the total over its sessions) and
  UNCOUNTED (sessions whose result was `missing`).
- **CREW** is new: one row per crew member with `crew_usage` in the
  window. Crew sessions burn the same windows, and leaving them out
  would make the beads look like the whole bill.
- `--json` carries all four counts for every row, under `usage`
  (`input`, `output`, `cache_read`, `cache_write`), plus `partial` and
  `uncounted`.

`yardr bead show` gains one line after `labels`, read from the bead's
`session_usage` events:

```
  tokens   1.06M new, 33.0M cache read · new muse 592k · review codex 235k · review-security muse 231k
```

When one of the bead's sessions is `missing` or `partial`, the total
takes `≥` and that session's entry says why, so the line cannot pass
for a complete count:

```
  tokens   ≥827k new, 31.2M cache read · new muse 592k · review codex missing (no file) · review-security muse 235k
```

There is no `yardr session show` to extend. Without one, a session's
count is in `yardr session list --all --json` (`handle.usage`) and,
per stage, on the bead's `tokens` line. Whether to add `yardr session
show <id>` is open question 6. Adding a TOKENS column to `session list
--all` would make a 12-column table wider still, and is left to a later
bead.

**Routes reading it.** A route that sends a costly label to a cheaper
group would read the same events. It is a later bead, named here and
not designed: *"Routes may prefer a group by tokens per bead for a
label."*

### 3.5 Against the window (question 5)

**No: a bead's tokens cannot be shown as a share of the weekly window
today.** aiquokka gives, for each window, `used_percent` (a whole
number: 11, 33, 29), `target_percent`, `resets_at` and
`duration_seconds` (read today for `claude`, `codex` and `muse`). It
gives no window size in tokens. `limits.Window` has `Used` and `Limit`
fields, but no provider fills them. Muse answered with no windows at
all ("has observed no usage yet"). Codex's `token_count` lines carry
the same percentages, not sizes. A share would also need weights. Claude
counts its windows by model (it has a separate "Weekly Fable" window),
and a cache read does not cost what an output token costs, so a window
is not a plain number of tokens.

The design therefore keeps tokens and the window side by side, and
does not divide one by the other. If a source later gives `limit` in
tokens for a window, a later bead can show the share. Estimating a
window's size from the yard's own counts (how far `used_percent` moves
per million new tokens over a week) is a separate later bead. Many
sessions overlap, and the percentages are whole numbers, so such an
estimate is only meaningful over weeks, not per bead.

### 3.6 Why the transcript and not OpenTelemetry

The OTel path (`docs/otel.md`) needs a collector the operator runs and
settings in every pane's environment. It has been documented for
Claude and Codex but never run, and Muse has no section. It delivers
one data point per request, which still has to be added up somewhere
other than the yard. The transcript is already on the machine for all
three harnesses that matter, needs no setup, and is read by the
process that owns the store. Its totals are Claude Code's own
(`cost-state`) and Codex's own (`total_token_usage`). The two paths do
not exclude each other: an operator who wants per-request detail keeps
the collector, and the yard's numbers do not depend on it.

## 4. Left out

- **Reading during the session.** Nothing in the question needs a live
  count: the report counts closed beads, and the crew's live context
  reading already exists. Following a file with a cursor (as Gas City
  does) adds state to keep consistent for every session, for a number
  nobody reads until the session ends.
- **One record per call, kept in yardr.** One total per session answers
  every question here. A per-call record would bring back Gas City's
  ledger, cursor and per-harness extractors.
- **Counts per model inside a session.** `cost-state` gives them;
  the session's model is already on the session record, and Claude's
  side calls are small (54k input in the largest case checked). Adding
  across models with `*` keeps one shape for every harness.
- **Reasoning and thinking as their own column.** All three harnesses
  count them inside output: Codex (`total_tokens` adds up without
  them), Claude (`cost-state`'s `outputTokens` exceeds its
  `thinkingTokens`), and Muse (reasoning never reached output in 65,841
  records, section 2). Muse's documentation does not say so, so build
  bead 1 pins it with a test on a real record.
- **For a later enterprise layer, only named here:** budgets per person
  or team that stop work when exceeded; dollar pricing for chargeback
  (Gas City's `internal/pricing` is that); rollups across linked yards;
  export to a billing or BI system.

## 5. Open questions

1. **Which number leads in the report and in `bead show`?**
   (a) New tokens: input, cache writes and output, without cache reads.
   Easy to compare across harnesses; leaves out the bulk of the traffic,
   which costs much less but not nothing against a window.
   (b) All four added together: honest about volume, but 88 to 98 per
   cent of it is cache reads, so the column mostly measures how long
   the context grew.
   (c) Two columns, NEW and CACHED: complete, and two columns wider.
   I would take (a), with all four counts in `--json` and in `bead
   show`'s line.
2. **Muse: match the file by worktree now, or wait for herdr?**
   (a) Build the `workspace` key (3.3): one more optional key and a
   matching rule, which also makes the model check work for Muse.
   (b) Ask herdr to report Muse session ids and wait: no matching rule
   to keep, but no Muse count (most of the yard's work) until it lands.
   I would take (a), and ask herdr as well. When herdr gives the id, it
   takes precedence and the match stays as the fallback.
3. **Should the count outlive the 90-day retention?**
   (a) No: it lives on the session record and in events, and is pruned
   with them, as the report's other columns are.
   (b) Yes: also keep a total per group on the bead record, which is
   never pruned. That needs a migration and a second place to write.
   I would take (a). The report already reads only what is retained,
   and 90 days covers the question.
4. **Should Claude's partial fallback also read the session's subagent
   files** (`<id>/subagents/*.jsonl`)?
   (a) No: the main file only, marked partial. Simpler; it falls
   further short when the session used subagents.
   (b) Yes: a second glob on the reading. It matched `cost-state`
   exactly on input and cache in the one case checked.
   I would take (a). The fallback is only for killed processes, and
   `partial` already says the count is short.
5. **Where do crew counts show?** (a) A CREW table in `route report`.
   (b) A column in `yardr crew list`. (c) Both. I would take (a): that
   report is where someone would look for what burned the window.
6. **Should there be a `yardr session show <id>`?** The bead names it,
   and it does not exist.
   (a) No: the count is in `session list --all --json` and on the
   bead's `tokens` line, stage by stage. Nothing new to keep.
   (b) Yes: a new command that prints one session's record (bead,
   group, stage, kind, model asked and reported, times, worktree) and
   its four counts, with `--json`. One session's work (build bead 9),
   but a new command whose token fields the bead's line already shows.
   I would take (a), and build (b) once something other than tokens
   asks for a view of one session.

## 6. The beads it would become

In the order they would land. Each is one session's work.

1. **Harness files declare where a session's token count is (`usage:`).**
   Goal: `agents.UsageSource` as in 3.3 (dotted `type_field`, `*` in the
   path, `read: last|sum` with `key`, the four fields,
   `cache_in_input`, `partial`), `Harness.Usage []UsageSource`,
   `validate`, and a `UsageIn` that turns one line into the four counts.
   The `usage:` entries go into `claude.yaml`, `codex.yaml` and
   `muse.yaml`, with comments saying where each was read. Where to look:
   `internal/agents/harness.go` (`ContextSource`, `UsageIn`,
   `validate`), `harness_test.go`. Done when: tests read trimmed lines
   taken from the three real files and get the figures in section 2
   (Claude `cost-state` 752 / 74,978 / 23,562,912 / 247,751 plus Haiku;
   repeated assistant lines counted once per `message.id`; Codex's last
   `token_count` with the cache subtracted; Muse's sum of records); a
   Muse record with reasoning tokens (the `output_tokens` 435,
   `reasoning_tokens` 337 record of `01a15ffe…`) reads as output 435, so
   a change that starts adding reasoning on top fails the test;
   `validate` refuses a `usage:` with no `model:` and one with no
   fields; `ContextSource` behaves exactly as before.
2. **The runner reads a session's id and its count.** Goal:
   `Herdr.SessionID(ctx, handle)`, the id `sessionKind` reads, on its own
   and whether or not the kind names a model; and `Herdr.Usage(ctx,
   handle)`, which reads the file named by `handle["agent_session"]` and
   returns the four counts with `partial`, or why it found none. It
   tries the kind's readings in order; a first reading that finds
   nothing is reported as "not yet", so the caller can wait. Where to
   look: `internal/runner/context.go`, `model.go`. Depends on 1. Done
   when: tests with an `AgentHomes` temp directory read each kind's
   fixture file; `SessionID` returns the id for a pane whose file has no
   model line yet; a Claude file without `cost-state` reads "not yet",
   and reads as `partial` when the caller allows the fallback; a missing
   file gives `no file`.
3. **The store records a session's count in one write.** Goal:
   `RecordUsage(ctx, sessionID, usage)` on `store.API`, daemon-only,
   which in one `changeSession` sets `handle.usage` and logs
   `session_usage` (added to `DispatchKinds`) for the session's bead,
   and does nothing when the handle already has a `usage`. `go generate
   ./internal/rpc` for the client. Where to look:
   `internal/store/routing.go` (`changeSession`, `SetHandle`),
   `internal/store/events.go`, `internal/store/api.go`. Done when: a
   store test shows the handle and the event after one call, nothing new
   after a second; a call whose transaction fails (a session id that is
   not there) leaves no event; a call from a pane is refused as
   `SetHandle`'s is.
4. **The dispatcher records each session's id and, after it stops, its
   count.** Goal: `Dispatcher.agentSession` writes `agent_session` on
   the handle of every running session that lacks one, within
   `modelWindow`, independent of the model check, and `teardown` asks
   once more before `Stop` and sets `kept: true` on a `keep: true`
   group's handle; `Dispatcher.usage` reads stopped sessions that have
   an id and no `usage`, waits 10 minutes for a missing first reading,
   then falls back, and calls `RecordUsage`, with `missing` when nothing
   was found. Where to look: `internal/dispatch/dispatch.go`
   (`teardown`), `model.go`, `crewcontext.go` (the `ContextReader`
   interface pattern). Depends on 2 and 3. Done when: dispatch tests
   with a stand-in runner show one `session_usage` per session; a
   session whose stand-in reports an id and never a model has its id on
   the handle, and after a new `Dispatcher` is made on the same store
   (the daemon replaced) and the stand-in no longer knows the agent, the
   count is still read by that id; the 10-minute wait is measured on
   the dispatcher's own clock (`d.now`); a `keep: true` session is read
   once, at its stop, and carries `kept`.
5. **Crew sessions are counted to their member.** Goal:
   `agentSession` also writes the crew handle; `CrewStoppedFor` and
   `CrewGoneFor` append `{agent_session, kind, started_at, stopped_at}`
   to a new `CrewSession.Uncounted` in the transaction that ends the
   session; `RecordCrewUsage` removes the entry and logs `crew_usage`
   `{member, user, kind, agent_session, usage}` in one transaction; the
   dispatcher reads each waiting entry as in 4. Where to look:
   `internal/store/crewsession.go`, `internal/store/crew.go`,
   `internal/dispatch/crew.go`. Depends on 4. Done when: a rotation and a
   gone session in dispatch tests each leave one `crew_usage` event and
   an empty `Uncounted`, the count read after the handle was cleared; a
   second `RecordCrewUsage` for the same id logs nothing; a Muse crew
   session with no herdr id reads `missing: no session id`.
6. **Muse sessions are found by their worktree** (if open question 2 is
   decided (a)). Goal: `ModelSource.Workspace` and the match in 3.3;
   `SessionID` uses it when herdr gives no id, so the model check and
   the count both gain Muse. Where to look:
   `internal/agents/harness.go`, `internal/runner/model.go`,
   `harnesses/muse.yaml`. Depends on 4. Done when: with two Muse fixture
   files for one worktree, each of two sessions is matched to its own
   file (the yardr-58xw case: starts 4 s and 3 s after their claims); a
   file that starts before `StartedAt` or after `modelWindow` is not
   matched; Muse's `model_reported` is filled in a test.
7. **The route report shows tokens.** Goal: TOKENS/BEAD, TOKENS and
   UNCOUNTED in the main table, TOKENS and UNCOUNTED in BY HARNESS, the
   CREW table, `≥` on every TOKENS cell that holds a partial or missing
   result, TOKENS/BEAD over fully counted beads only, and `usage`,
   `partial` and `uncounted` in `--json`, all read from `session_usage`
   and `crew_usage` events through `sessionOf`. The command's help is
   updated to match. Where to look: `internal/cli/routereport.go`,
   `routereport_test.go`. Depends on 4 and 5. Done when:
   `summarizeRouteEvents` tests cover a cell with counted, partial and
   missing sessions (it prints `≥`, UNCOUNTED 1, and a median that
   leaves the bead with the missing session out), a bead two groups
   worked, and a crew row; the help text describes each new column.
8. **`yardr bead show` prints the bead's tokens.** Goal: the `tokens`
   line from 3.4, from the bead's `session_usage` events, with `≥` and
   the reason on a missing or partial session, and the same in
   `--json`. Where to look: the bead show command in `internal/cli`.
   Depends on 4. Done when: a test shows the line for a bead with three
   counted sessions, the `≥` line for a bead with one counted and one
   missing session, and no line for a bead with none.
9. **`yardr session show <id>`** (only if open question 6 is decided
   (b)). Goal: a `show` subcommand beside `list` and `snooze` (the
   `session` command in `internal/cli/ops.go`) printing the session
   record and its counts, with `--json`. Depends on 4. Done
   when: a test prints a counted, a partial and a missing session.

Named and not designed: routes that prefer a group by tokens per bead
for a label; the share of a window, once a source gives a window's size
in tokens; estimating a window's size from the yard's own counts; the
enterprise items in section 4.
