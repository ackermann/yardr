# Dispatcher pass stops waiting on slow outside calls

Design yardr-frbj, proposed 2026-10-26. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead.

## 1. The question

From mckean's external review (Hyperagent, yardr @ 20e8ded, 18 Oct, finding
A2), as the bead quotes it: "one serial dispatcher pass waits on slow outside
calls, so one hung remote, herdr call or limits provider stalls reaping, crew
and every group." The design asked for is three moves, and no build in this
bead: starts off the pass (a per-group worker, keeping count, active and
running bookkeeping honest across restarts), limits served from a cache
refreshed in the background, and `settleSent` handed to a background worker
(`SettlePeerBeads` already sweeps). Terms used below. A *pass* is one
`Dispatcher.Reconcile` (internal/dispatch/dispatch.go): reap, usage, teardown,
cleanup, timers, waits, sidings, crew, hook rules, CI, issue moves, pushes,
peer bases, route, backups, peers and fetches, in that order, on one goroutine.
The *store* is the single bbolt file one process owns (internal/store); every
other process reaches it over the unix socket (`POST /call/<Method>`,
internal/rpc). A *session* is one agent working one bead in one stage, stored
with state `starting` and then `running`. The *limits command* is the
operator's command a source declares under its `limits` key
(internal/limits), run by the daemon alone. yardr's principles apply:
prefer a mechanism that exists over a new one, and a general one over a
special case.

## 2. What exists today

Everything in this section can be opened.

### 2.1. One pass, one goroutine, and the store as the only durable truth

`Dispatcher.Run` (internal/dispatch/dispatch.go) reconciles once on start,
then once per store wake, runner wake or `Interval` tick (30 seconds by
default). A wake only triggers a pass; every pass re-reads state
from the store and acts idempotently, so a missed notification costs a delay,
never a lost bead, and a restart is just another pass. `Reconcile` runs its
steps strictly in sequence and returns before the next pass begins:
nothing in a pass runs beside anything else in it. That serial shape is the
finding: any step that waits on the outside waits with every later step, and
with the next pass, behind it.

The `Dispatcher` value itself is built for that single goroutine. Its maps
(`retryAfter`, `reported`, `status`, `unknown`, `down`, `idleSince`,
`limitsAsked`, `peerWait` and the rest) carry no lock and are read and written
only from the pass, except the home snapshot (`homeMu`, internal/dispatch/
homesessions.go) and the quiet run's flag (`quietMu`, internal/dispatch/
quiet.go), which already have background readers. Any design that adds
goroutines must say, for each piece of pass state it touches, who owns it
now; section 3.1 does that for the start path, which today reads and writes
several of those maps in one call.

Two background mechanisms already live beside the pass, and the proposal
reuses their shapes rather than inventing new ones. Each peer that can be
called gets a *puller*: one goroutine per peer, started and stopped by the
pass from `Run`'s context (`pullers` and `pullFrom`,
internal/dispatch/peers.go), which holds no pass state between questions,
reads the peer and the yard's key again each time, and wakes the loop through
`poke` when something arrives. The quiet whole-suite run likewise runs on its
own trigger goroutine (internal/dispatch/quiet.go), guarded by `quietMu` and
a single `quietRunning` flag, and never judged by the pass's tick. Inside the
store, the *pump* (internal/store/store.go) is the one background goroutine
the store owns today: writers only nudge it after commit, it publishes events
in sequence order, and `Close` stops it through `done` and waits on `pumped`.
`Exec.Start` (internal/runner/exec.go) already reaps its child on a goroutine
of its own. Background work is not new to this codebase; work that starts
sessions is the part that has never left the pass.

### 2.2. Starts run inline in `route`, after everything else

`Reconcile` reaches `route` (internal/dispatch/dispatch.go:1242) late in
the pass, after reap, crew, rules, CI, issues, pushes and bases, as
the bead says. For each dispatchable bead `route` matches a route, picks a
group through `chooseGroup` (section 2.4), checks the group's wait and the
yard's `max_sessions` cap, reads the bead whole, resolves its next stages and
family, claims it in the store (`Store.Claim`,
internal/store/routing.go:2332), and then calls the runner where it stands:

```go
handle, err := runner.Start(ctx, Job{Bead: b, Group: g, Depot: depot, Session: *sess, ...})
```

The pass does not move to the next bead, let alone the next pass, until that
call returns. What one `Start` can wait on depends on the runner
(internal/runner/runner.go, `All`):

| Runner | What `Start` waits on today |
|---|---|
| `herdr` | A fetch of the depot's base (`FetchTimeout`, 60 s, internal/runner/herdr.go:505), a train-branch check, worktree create or open, a pull-request head fetch (again 60 s), then tab creation, brief writing and agent start. Every herdr CLI call (`run`, internal/runner/herdr.go:132) and every API-socket call (`call`, internal/runner/herdr_api.go:26) carries its own 2-minute timeout, and every git call in the checkout path (`gitOut`, internal/runner/herdr.go:810) its own 2 minutes (`checkoutTimeout`, herdr.go:797). A single healthy start makes roughly half a dozen such calls in sequence; a sick remote or a herdr that answers slowly multiplies them. Before any of that, `followSetting` (internal/runner/herdr_session.go:224) reads the yard's settings from the store on every call. |
| `exec` | Reads the group's `env_file`, opens the session log and forks `sh -c` (internal/runner/exec.go:83). Fast unless the filesystem is; no network. |
| `manual` | Records the handle and, where the group asks, shows a notification (internal/runner/notify.go:45). No outside wait to speak of. |
| `peer` | Loads the yard's key file and has the store queue the bead for its peer (internal/runner/peer.go:33). Store and file only. |

Crew starts wait the same way one step earlier: `crew`
(internal/dispatch/crew.go:80) calls `StartCrew` inline from
`keepSession` for each member that needs a session, and the herdr
implementation (internal/runner/herdr_crew.go:168) resolves the member's
session environment (`env_file`, `env_command`, the harness key) and then
makes the same workspace, tab and agent-start calls as a bead start. One slow crew start therefore stalls routing too, though
the bead names bead starts; section 3.1 says how crew follows, and Open
Question 5 asks whether it must land first.

A failed start is classified where it returns, still inside the pass. A
`BeadBlockedError` ends the session as `aborted`, holds the bead and logs
`session_blocked`. A `RuntimeError` or `BlockedError` aborts the session,
records the group's outage (`runtimeDown`: `down` by group, doubling from
`RuntimeBackoff` 30 s to `RuntimeBackoffMax` 5 minutes) and returns the bead
for a later pass. Any other error ends the session as `failed`, which counts
against the bead's `MaxAttempts`. A start that opened a herdr tab and then
failed hands its partial handle back, and the pass keeps it for teardown and
for the home snapshot (internal/dispatch/dispatch.go:1459). Every one of
those outcomes is store truth within the same pass that claimed the bead;
section 3.1 keeps that property while moving the wait.

### 2.3. The counts a start must keep honest

Three counts decide whether a bead may start, and a design that moves the
start moves where each is kept.

The store's count is the honest one. `Claim` runs inside its own write
transaction, counts the group's live sessions through the live index
(`bSessLive`), and refuses with `ErrAtLimit` at the group's `Limit` "so two
dispatchers (or a dispatcher and a stale in-memory count) cannot both take
the last slot" (internal/store/routing.go:2355). A session is live in state
`starting` as well as `running` (`Session.live`,
internal/store/routing.go:2077), so a claimed bead holds its slot from the
claim, before anything has started. `MarkRunning`
(internal/store/routing.go:2467) only fills in the handle and moves
`starting` to `running`; `EndSession` returns the bead and, for `failed` and
`died` but not `aborted`, counts a failure. Because the claim comes first in
today's code too, claiming earlier and starting later changes no store
semantics at all, provided nothing claims a bead it does not then try to
start.

The dispatcher's two in-memory counts are fast paths over that truth, rebuilt
or repaired every pass. `active`, the live sessions per group, comes back
from `reap` (internal/dispatch/dispatch.go:935), which asks each live
session's runner whether it is alive and counts it through `live`; `route`
then repairs it as it claims and as starts fail, through the `count` closure
(internal/dispatch/dispatch.go:1439), and does the same for `running`, the
sessions that run on this machine against `max_sessions`
(`store.MaxSessions`). Both are pass-local: they are never carried between
passes except as `running`'s field copy, and the next pass rebuilds them from
the store's sessions regardless. A start that finishes after its pass has
ended therefore has to leave the store right and let the next pass recount;
it must not reach into a later pass's `active`, which belongs to that pass.

Restarts are already honest about a start that never finished, because the
store holds the session. `reap` treats a `starting` session seen by a later
pass as a dispatcher that died mid-start: younger than five minutes it is
counted live, older it is ended as `failed` with reason `start interrupted`
(internal/dispatch/dispatch.go:959). Teardown then stops what the recorded
handle names (`Unstopped`, `MarkStopped`), and a partial herdr handle saved
by `SetHandle` on a failed start is what lets it close the tab that was
opened. Nothing about that needs inventing; it needs not breaking. Five
minutes is shorter than a healthy start's worst case, and the arithmetic
matters for section 3.1, so it is worth doing here. `Herdr.Start`
(internal/runner/herdr.go:170) is a sequence, not one call: a base fetch
bounded at 60 seconds (`FetchTimeout`, herdr.go:505), the local-base
variant's several `gitOut` calls and the train-branch checks (each
`checkoutTimeout`, 2 minutes, herdr.go:797), the worktree create through
`run` (2 minutes, herdr.go:132) with its open, re-add and re-open fallbacks
(each another bounded call), a pull-request head fetch (60 seconds) and
`rev-parse`, the `tab.create` socket call (2 minutes,
internal/runner/herdr_api.go:26), the tab close, and the brief's agent
start, itself a chain of `run` and `call` steps. The longest healthy path
therefore sums to well past ten minutes of calls that are each inside
their own bound, and the fallback paths add more. Today that start is safe
from the five-minute rule only because the same pass that began it is
still waiting on it, and no other pass can reap it. The moment starts
outlive their pass, a later pass of the same daemon can see a healthy
`starting` session age past five minutes while its worker is still
working. Section 3.1 answers that by splitting the rule on ownership — a
`starting` session a live worker of this daemon owns is counted live at
any age; the five minutes judge only a session no worker owns, which is
exactly the dead-dispatcher case the rule was written for — and not by
picking a longer grace or a shorter start.

Around those counts sit the pass-only maps a start's outcome feeds today:
`down` (the group's outage, read by `waitReason` before any claim),
`reported` (one `quota_wait`, `group_wait` or `unrouted` event per bead,
stage and route until it starts), `status` and the idle maps (forgotten
through `forget` when a session ends), and the home snapshot, which
`noteHomeStarted` already writes under `homeMu` into both the finished and
the in-progress snapshot "so that a pass which never ends still leaves it
behind" (internal/dispatch/homesessions.go:137). The snapshot is the one
start outcome already written to be safe from another goroutine.

### 2.4. Limits are asked inside the pass, and yardr-7q9z widened who asks

A source of the yard whose file carries a `limits` key names the command
that says what a harness subscription still carries; `limits.Timeout` (30
seconds, `source.Timeout` in internal/source/call.go:28) bounds one run,
kills its whole process group at the deadline, and is long on purpose,
because a provider that renews a stored login as it reads it must never be
cut between the read and the write (internal/limits/limits.go:42). Nothing in
internal/limits asks by itself, remembers an answer, or repeats an ask; the
dispatcher decides when, and remembers in `limitsAsked`, keyed by source,
provider name and a digest of the group's environment overlay, so secrets in
an `env_file` never sit in a key (internal/dispatch/harness_limits.go:84).

The dispatcher runs that command in three places, all inside the pass, and
the file says so plainly: "a command that hangs holds one pass for
limits.Timeout, once in limitsMemory" (harness_limits.go:39). `routeQuota`
(internal/dispatch/quota.go:166) serves a route's condition: the store holds
the route and makes the comparison (`Route.Choose`), and only the daemon may
learn what the windows are. `harnessLimits` (harness_limits.go:183) decorates
a `session_harness_error` of reason `rate_limited` with the window the agent
waits for, and `windowOpen` (internal/dispatch/harness_reset.go) asks again
when that window's reset has come. The route read remembers for
`routeLimitsMemory` (5 minutes, "at most
twelve times an hour for a provider, however many beads are ready",
quota.go:24) and the other two for `limitsMemory` (60 seconds); an answer
that says how long it holds (`Report.HoldsFor`) shortens either. A provider
that fails is remembered as failed for the same
age, so it is not asked five times either (harness_limits.go:49).

How the two beads fit. yardr-7q9z ("Start no session of a group while a
window of its provider is used up, condition or none", merged) changed who
is gated, not when the command runs. Before it, only a route with an `@`
condition asked at all; its `chooseGroup` dropped the `Quotaed` shortcut for
any route `Route.Gated` names, one with a condition or with a group that
starts agents, and `Route.Choose` judges a group with no condition by an
implicit `QuotaLimit{Under: 100}`, so a plain route to an agent group now
asks too, fails open on unknown limits with `limits_unknown` on the claimed
event, and stays silent on a good report with no windows. Exec, manual and
peer routes still ask nothing, and a bead that would wait anyway (every
group at its limit, its runtime down, held back for its model) asks nothing
either: the `free` pre-check in `chooseGroup` runs before any ask. The
consequence for this design is that the population of asks grew from
conditioned routes to nearly every agent start, while the asking stayed
synchronous. A hung provider on a plain route therefore stalls the pass
exactly as the review found, only now on behalf of beads that never wrote a
condition. The cache in section 3.2 changes when and where the command runs
and nothing else: `Gated`, `Choose`, the implicit comparison, fail-open and
the silent no-window report all keep their meanings, and the five-minute
route age stays the route's freshness, not a new one.

### 2.5. `settleSent` runs inside `Store.write`, after the commit

When a bead's stay at a peer ends, the write that learns it appends the bead
to `wtx.settle`: `endSent` (internal/store/peersend.go:595) when the outcome
arrives, and `PeerFetched` (peersend.go:1158) when its returned branch has
been taken. `Store.write` (internal/store/store.go:320) then commits through
`db.Batch`, nudges the pump, pings the hub, and only then, still inside the
call that every writer waits on, settles each bead where it stands:

```go
for _, uuid := range settle {
    ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
    _ = s.settleSent(ctx, uuid)
    cancel()
}
```

`settleTimeout` is 30 seconds (store.go:349), per bead in the write.
`settleSent` (internal/store/peersend.go:842) reads the bead, resolves its
flow through `repoFor`, which reads the depot's repository flows when the
flow lives there (`RepoFlows`, internal/store/repoflows.go:276: a
`rev-parse`, an `ls-tree` and one `git cat-file --batch`, cached by base
commit), works out the stage's home, and applies the outcome in a write of
its own: the peer's notes added, the outcome reported as `Done` reports one,
the bead moved or held. None of that git work runs inside a transaction; the
code is careful about that everywhere else (`Claim` reads its blockers'
repositories before its write for the same reason). But it runs inside the
writer's call, after the commit and before the answer, so the socket call
that ended the stay, a `PeerReceive` or a `PeerFetched` through dispatch,
waits out the git reads with a 30-second bound and then swallows whatever
went wrong: `_ =`. The write itself is done either way, as the comment says;
what waits is the caller, and what is lost is the error.

The backstop already exists and already works. `SettlePeerBeads`
(internal/store/peersend.go:818) applies every known, unapplied outcome, and
the daemon calls it at the head of `peers` on every pass
(internal/dispatch/peers.go:64), with the pass's context and no per-bead
bound. A settle the write could not apply, because its depot's repository did
not read, stays exactly as the write left it and is applied there; nothing is
lost to a process that died between, because `Settled` is only set inside
`settleSent`'s own write, after the pending check is repeated inside that
write. The synchronous loop buys promptness, a bead moved on before its
writer's answer returns, and pays for it with every writer's latency and a
hidden place where git runs. Section 3.3 keeps the promptness and drops the
payment.

### 2.6. What else waits in a pass, named so the scope is honest

This design takes three waits out of the pass. Others stay, and the reader
should be able to see them. Two of them have since left it: limits
commands went behind the background-refreshed cache (bead 1, yardr-sybk),
and starts went to workers, bead starts to one per group (bead 3,
yardr-jzhq), which claims in the pass and runs `runner.Start` beside it
under each call's own bound, one start in flight per group, and crew
starts to one per crew member (bead 4, yardr-pv12), which decides in the
pass and runs `StartCrew` beside it the same way, one start in flight per
member across its line and conversation sessions. What a pass still waits
on is:

| Wait still in the pass | Bound today |
|---|---|
| `reap` asks each live session's runner for its status, in sequence (`alive`, then `Status`: one herdr call per session, 2 minutes each), reads pane ends for blocked, idle and stopped agents, and applies idle and age limits (`limits`, internal/dispatch/limits.go:79); the limits those apply are read from the background-refreshed cache, so no provider command runs here | 2 minutes per session, times the live sessions, worst case |
| `teardown` and `cleanup` stop runners and finish worktrees (`Stop`, `Finish`: herdr calls and git at `checkoutTimeout`) | 2 minutes per call |
| `usage` and `crewUsage` read stopped agents' session files for token counts | filesystem only, retried next pass |
| `crew` keeps the members' live sessions (status reads, the nudges it types, the stops of a restart and what a failed start left open) and `harnessLogins` may run this binary's `harness login`; the start itself is off the pass | herdr bounds, plus the login command's |
| `rules`, `makes` put hook events to their tables, which can run declared sources' commands | each source call's own timeout, with per-source waits after failure |
| `ci` reads a train's checks through its source; `issues` moves tracked issues through theirs | source timeouts, skipped after failure through `ciWait` and `issueWait` until `Backoff` to `BackoffMax` |
| `pushes` tells users outside the yard through their source (`tell`, internal/dispatch/pushes.go:86) | `PushTimeout`, 10 seconds a send, twice that with the picture fallback, per user |
| `bases` and `fetches` run git for peer beads' bases and returned branches (`KeepPeerBases`, `TakePeerBranch`) | `branchTimeout`, 60 seconds each |
| `peers` posts and asks peers with a URL, at the pass's end ("a peer that does not answer holds up nothing of this yard's") | `peerTimeout`, 30 seconds a request, 16 messages a peer; the held questions themselves already run on the pullers |
| `backups` copies the store file when one is due ("a copy of a large store takes a while") | the copy itself |
| `route` asks which open beads sit in a stage their flow lacks, in each pass that has a ready bead: `Store.Stranded` (internal/store/flows.go:974) decodes every open bead and, for a bead whose flow is in its depot's repository, reads that checkout through `RepoFlows` (flows.go:1022; internal/store/repoflows.go:276) | no bound of its own in the store's git; the pass's context is the only one, and the cache by base commit makes one `rev-parse` the common cost |
| `route`'s `nextStages` resolves each bead's flow before its claim: `Store.NextStages` (internal/store/flows.go:1317) and `Store.FlowMap` (internal/store/flowmap.go:219) each call `repoFor` (flows.go:1171) into `RepoFlows`, so a bead whose flow is in its repository pays that lookup twice | the same: no bound of its own, cached by base commit |
| `route`'s claims read the blockers' repository flows before the claim's write: `Store.Claim` calls `untilBlockerRepos` (internal/store/routing.go:2340) which, for each blocker on an `until` edge, may call `repoFor` into `RepoFlows` (internal/store/flows.go:877, 1171; internal/store/repoflows.go:276) — a `rev-parse`, an `ls-tree` and one `git cat-file --batch` over the depot checkout, cached by base commit so the common claim pays one `rev-parse` | no bound of its own in the store's git; the pass's context is the only one, and the cache makes it rare |

Two of those already carry the pattern this proposal follows: a source that
just failed is not asked for the next bead in the same breath (`issueWait`,
`ciWait`, `peerWait`, and the runtime outage in section 2.2), and the peers'
held questions left the pass entirely when they learned to hold. The reap's
per-session herdr calls are the largest wait left after this design, larger
than any of the three it removes once a yard runs many sessions. They are
left out (section 4), deliberately and by name, not overlooked.

The pass measures these waits itself now (bead 5, yardr-cf76). `Reconcile`
marks its steps by name, and a pass that runs past one `Interval` logs one
`slow pass` line at Warn: how long the pass took, how many sessions it found
live, and each step's time in the order the steps ran
(internal/dispatch/passtime.go). A pass inside the interval logs nothing.
Route's flow lookups are timed at the pass's call sites and not in the
store, whose API does not move for measurement: `Stranded`, `NextStages` and
`FlowMap`, and `Claim`, which reads the blockers' flows inside the call, so
its time is the whole call's, its write included. Each appears in the line
with its total and how many times the pass made it, and each is inside the
`route` step's time, not beside it. The line is names and numbers only: no
bead, session handle or credential.

### 2.7. What a person sees today

Nothing names the pass's waits. `yardr session list` shows a session as
`starting` only in the instant between claim and `MarkRunning`, which today
is the length of one `Start`; a bead whose group waits on a window shows one
`quota_wait` event with its `resets_at`, and the next ready bead behind a
hung fetch shows nothing at all: `route` reaches beads in sequence and is
still inside the hung `Start`, so that bead has not been matched, claimed or
counted yet, and the pass that would say anything is the one that is stuck.
The daemon's log says `dispatching` and then, eventually, the outcome. A peer
bead's return shows as `peer_bead_returned` and the move that follows it in
the same breath, because the write settled it. The proposal keeps every one
of those surfaces and changes only their timing, apart from the cold and
stale limits cases section 3.4 names; section 3.4 says what each looks like
after.

## 3. The proposal

Three moves, each carried by a mechanism that already exists, and no new
command, setting or stored state. Starts move to one worker per group,
modelled on the peers' pullers: goroutines owned by `Run`, holding no pass
state, waking the loop when they finish. Limits move behind the memory the
dispatcher already keeps, refreshed by a background refresher on the same
ownership rule, so the pass only ever reads. `settleSent` moves behind the
store's own pump pattern: a settler goroutine in the store, nudged by the
write that queued the bead, with `SettlePeerBeads` kept as the sweep that
already runs every pass. The store stays the only durable truth in all
three; nothing is remembered only in a goroutine, and a restart is still
just another pass, or another sweep. In landing order the three moves are
independent in code and sequenced so each can be measured alone
(section 6): limits first, because yardr-7q9z made its stall the most
common; the settler second, because it unblocks writers outside the
dispatcher entirely; starts last, because they touch the most pass state.

### 3.1. Starts off the pass: claim in the pass, `Start` on a per-group worker

Keep every decision where it is. `route` still matches, still runs
`chooseGroup` (now against the cache of section 3.2), still checks
`waitReason`, the yard cap, `backingOff` and the failure count, still reads
the bead whole, its next stages, family and pull, and still claims it with
`Store.Claim`. The claim is the reservation: from that instant the session
is live in the store, holds its group's slot against every other claim, and
survives any restart. Only the line after the claim changes. Instead of
calling `runner.Start` where it stands, `route` hands the finished `Job`,
with the claimed session, to the worker of the bead's group and moves to the
next bead. The `count(1)` repair of `active` and `running` still happens at
the claim, in the pass that made it, exactly as today; the pass's counts are
correct for the rest of that pass, and the next pass rebuilds both from
`reap` regardless.

One worker per group, one start in flight per worker, and no queue. A
worker is a goroutine started by `Run` for each group name the yard has,
started lazily when the first job for that group is handed over and stopped
when `Run`'s context ends, as a puller is for each peer
(internal/dispatch/peers.go:173). It takes jobs from its own channel, one
at a time, and for each calls `runner.Start` with a context derived from
`Run`'s, never from the pass that claimed the bead: a pass ends, a start
does not. The dispatcher keeps, in pass-owned state the worker never
touches, the one session each group currently has in flight. `route`
consults it before it claims: a group with a start in flight is not
claimed for in this pass at all, and its next bead stays dispatchable and
unclaimed, with no session in the store yet. The hand-over channel may
carry the one job being handed over; that buffer is hand-over slack, not
a queue, because the pass sends for a group only while that group has
nothing in flight, and sends again only after it has drained that start's
completion (below). A worker therefore never holds a second claimed job,
and never starts one on its own motion. Single flight with no queue is
the load-bearing choice. It bounds a hung runtime to its own group, it
keeps a group's starts in claim order, it gives `waitReason`'s outage
check a simple meaning (a group whose worker is busy or down starts
nothing more this pass, and says so through the same `group_wait` shape a
full group uses), it cannot pile `starting` sessions up faster than the
runtime can take them, and — the point the queue would have broken — it
keeps today's outage order intact: at present a start that fails as a
runtime failure calls `runtimeDown` (internal/dispatch/dispatch.go:1489)
before `route` considers the next bead, and `waitReason` then holds the
group (internal/dispatch/quota.go:119). With a queued job the worker
could start that job in the gap between the failure and the next pass's
drain, before the outage was ever recorded. With no queue there is no
job in that gap: the next claim for the group is made by a pass that has
already drained the failure and applied the outage. Concurrency across
groups is the whole win: a herdr fetch hanging for the builders no longer
stands in front of a reviewer's start, the crew, the reaping or the next
pass.

What the worker does with an outcome is, store first, exactly what the pass
does today, in the worker's own goroutine and before it takes the next job:
on success, `MarkRunning` with the handle; on a partial herdr handle,
`SetHandle` so teardown can still close what was opened. One record is not
a store write and is not conditional on either of those succeeding:
whenever the handle holds a home tab — on success, and on a failure after
the tab opened — the worker calls `noteHomeStarted` for it (for a herdr
start, before it attempts `MarkRunning` or `SetHandle`), exactly as the
pass does today ahead of `MarkRunning` (internal/dispatch/dispatch.go:1512)
and for a partial handle (dispatch.go:1462). `noteHomeStarted` is already
written under `homeMu` for a caller that is not the pass, into both the
finished and the in-progress snapshot "so that a pass which never ends
still leaves it behind" (internal/dispatch/homesessions.go:137); "store
first" must not make it wait on, or be skipped by, a failed `MarkRunning`
or `SetHandle`, or the home-loss cleanup would lose a tab this design still
opens. On failure it classifies as the pass does now
and writes the same store truth before anything else: a blocked bead ends
its session `aborted`, logs `session_blocked` and holds the bead; a runtime
or blocked-group failure ends the session `aborted` and records nothing
against the bead; any other failure ends it `failed`. Those writes are the
store's, safe from any goroutine, and they are what the next pass reads.

What the worker never touches is the pass's own maps: `down`, `reported`,
`status`, the idle maps and `prompted`'s bookkeeping stay owned by the pass
goroutine, as every map in the `Dispatcher` is today. Instead the worker
posts one small completion (group, session, class of outcome, error) to a
channel the dispatcher owns, and the pass drains it at the head of
`Reconcile`, before `reap`. There, in the pass and nowhere else, it runs
`runtimeDown` or `runtimeUp`, forgets the session's idle state, and logs the
`dispatching` follow-ups it owes, in the order the outcomes arrived. The
only cost is that a group's outage is recognised at the next pass rather
than the instant its start fails; `waitReason` already reads `down` only to
decide the next claim, and the store has held the bead honestly in between.
A daemon that dies between a worker's store write and the next pass loses
nothing: the completion was never the record, the session is.

Three honesty rules keep restarts and races where they are today. First,
nothing is claimed without being handed over, and nothing is handed over
while its group already has a start in flight: the hand-over is a channel
send inside `route`, gated on the pass-owned in-flight record above, so
at most one session per group sits in `starting` because of this design,
the one its worker is starting. There is no queue, per group or global.
Second, the worker's context for one start carries no overall bound of
its own, and that is a derivation, not an omission. Section 2.3 sums the
longest healthy `Herdr.Start` path: a 60-second fetch, several 2-minute
`run`, `call` and `gitOut` steps, and fallback steps that add more, each
inside its own bound, in sequence — well past ten minutes. Any aggregate
bound short enough to end a wedged start sooner than that sum can also
end a slow but healthy start whose every call was inside its bound, and
the four minutes an earlier draft of this design proposed did exactly
that with no arithmetic behind it. So the per-call bounds stay the only
bounds, as they are today: the chain is finite, each step either makes
progress or fails at its own bound, and a start that makes slow progress
forever is a chain of steps each answering within its bound — bounded
step by step, never by a clock that cannot tell it from a healthy one.
Open Question 4 puts the one remaining choice, whether to add an
aggregate anyway and pay for it in slow depots, to the decider with
those costs stated. Third, `reap`'s `starting` rule splits on ownership
and otherwise does not change. The worker records its session in a set
the dispatcher owns, guarded by the one mutex that set needs (the worker
adds the session before it calls `Start` and removes it after its store
writes; the pass reads the set under the same mutex, and no pass map
crosses over). `reap` counts a `starting` session in that set live at
any age: its worker is working, and the five minutes were never a
verdict on slow starts. A `starting` session in no worker's set — every
one of them, in a daemon that has just restarted over the previous
daemon's in-flight start — is counted live for five minutes and then
failed as `start interrupted`, and teardown closes what its handle
names, exactly as a dead pass is handled today. The window in which a
restarted daemon sees the previous daemon's in-flight start is the
window that already exists, unchanged, because a restarted daemon owns
no worker for it. A daemon that stops with a start in flight leaves the
session `starting` and unowned, and the next daemon judges it by the
same five minutes; nothing about shutdown needs a new rule.

Crew starts follow the same rule on the same machinery, as a second step.
`keepSession` stops calling `StartCrew` inline and hands the member and user
to a worker keyed by crew member, one per member, with the conversation
sessions of one member in flight one at a time as today and, as for bead
starts, nothing queued behind a busy member's worker: a later pass asks
again. `CrewStartedFor`,
`CrewStartingFor` and the partial-handle path stay store writes the worker
makes; the pass-side results (`crewStartFailed`'s counting, the `status`
key, `closeLeft`) are applied from the same drained completions. Until that
step lands, crew keeps its inline start and remains one of the waits named
in section 2.6; section 6 sequences it deliberately, and Open Question 5
asks the decider to confirm the order, because the yardmaster's own session
is a crew session and a stalled crew start is the stall a person feels
first.

No runner changes. `Herdr.Start`, `Exec.Start`, `Manual.Start` and
`Peer.Start` keep their signatures and their timeouts; `followSetting`
still reads the yard's setting per call, now from the worker's goroutine,
which the store already serves from any goroutine. `Herdr`'s own maps
(`dismissedDialogs`, `stops`) already carry their mutexes. A runner that is
not safe for one call at a time per group need not become so: the worker
gives it exactly the serialisation the pass gave it, per group, and calls
for different groups already ran in sequence today only because the pass
did; the one new concurrency is two groups' runners at once, which are two
`dispatch.Runner` values or two groups sharing one, and the built-in herdr
runner is the shared case to test (section 6, bead 3).

### 3.2. Limits from a cache the background keeps fresh

The pass stops running the command, in all three places. `askLimits` and
`askSource` split into a read the pass may make and a run only the refresher
makes. The read, `cachedLimits`, looks up the same key as today (source,
provider name, environment-overlay digest) in the same `limitsAsked` memory,
applies the caller's age and the answer's `HoldsFor` through the same
`fresh`, and returns one of three things the readers must not confuse: a
fresh report; a fresh remembered failure, where the provider was asked
within the age and gave no answer; or no fresh answer, where the key was
never asked or its answer has gone stale and a refresh is due or already
running. It never runs the provider command and never waits for one. It is
not over in a map lookup alone: like `askLimits` today it first resolves
the answerer on every read — `limits.Answerers` reading `Store.Sources`
(internal/limits/limits.go:216), then `Resolve` or `SelfAnswerer` — and a
provider-group read still reads the group's `env_file` through `groupEnv`.
Those are a store read and a file read, and they are the whole cost the
guarantee needs: no command is run, and no read waits on one.
A fourth outcome is not a cache miss at all, and must not be treated as
one: the answerer cannot be resolved. No source names the kind, provider
or source asked for (`*limits.NoSource`, harness_limits.go:111, returned
today before any cache key exists, so nothing was or could be run), the
`Store.Sources` read behind `Answerers` fails, or a session's `env_file`
cannot be read (`sessionLimits` and `sessionWindowOpen` already turn that
into an unknown or a prompt respectively). There is no key, no command and
nothing for the refresher to refresh, so `cachedLimits` returns that error
at once, registers no want and starts no run. Each reader then does exactly
what it does with that error today: a route reads it as unknown and fails
open, a stop writes `limits_unknown`, and a reset prompts (the reset
paragraphs below). A source declared later is picked up by the next read's
own resolution, with no stale want to clear first.
`routeQuota`, `harnessLimits` and `windowOpen` call it exactly where they
call `askLimits` today, with the same ages (`routeLimitsMemory` for a route,
`limitsMemory` for a stopped or resetting agent). What is untouched is the
fresh-report meaning, and for routes the unknown meaning too:
`Route.Choose` still fails open on an unknown, the claimed event still
carries `limits_unknown`, a good report with no windows still stays silent,
and the `free` pre-check in `chooseGroup` still decides before any read
whether a bead that would wait anyway says `group_wait` instead of
`quota_wait`. The two harness readers do not keep every cold and stale
meaning, because today they wait for the command and so always decide on a
fresh answer or a fresh failure; the paragraphs after the refresher state
what each of them does on no fresh answer, and Open Question 2 puts that
choice to the decider.

The refresher is one scheduler goroutine owned by `Run`, on the puller
pattern. Every read that finds its key missing or stale notes the key as
wanted, with the smallest age any reader asked for and the time it was
wanted, and wakes the refresher; the scheduler starts each wanted command
whose memory has gone stale at the age its readers asked for, and sleeps
until the next key comes due or a new want wakes it. Runs are concurrent
across keys and single-flight within one: the scheduler starts a due key's
run on its own goroutine and never queues one key behind another's, so no
fairness order is needed and no key waits for a different provider's
`Timeout`. That concurrency is what backs the reset bound below: a key a
deferred reset waits on is either already running or started when its read
wants it, and in either case lands within one `limits.Timeout` of that
start, not after a queue of other keys' runs. The fan-out is bounded by
the distinct keys wanted at once — one per (source, provider,
environment-overlay) or named source, shared by every bead, group and
session reading it — not by the beads or sessions asking; five sessions of
one kind stopping together, or fifty beads becoming ready, still make one
run of one key, as the memory makes them one ask today. Concurrent runs of
distinct keys are distinct providers, logins or sources, and each already
carries `limits.Timeout` and its process-group kill. A want is a lease,
not a subscription. A key
stays wanted only while readers keep wanting it: each read renews the
want, and the refresher drops a key whose last want is older than the age
its readers asked for, running it at most once more if it came due in
between. That one rule covers every reader. A route key is wanted while
ready beads keep making passes read it; an idle yard whose beads have all
started stops renewing, and the refresher stops running its providers. A
key a stopped or resetting agent needs (`harnessLimits`, `windowOpen`) is
wanted only while that session still stands in the state that reads it;
once the session ends or moves on, its reads stop, its want lapses, and
the command is not run again for an agent that no longer needs the answer.
This matters beyond tidiness: asking is not harmless, because a provider
may renew a stored login as it reads it (internal/limits/limits.go:16), so
a key kept wanted forever would run its command every minute for the life
of the daemon, renewing that login each time, for nobody. At `Run`'s start
the refresher warms the keys the yard is known to need, concurrently: the
gated groups' providers under their groups' environments, the crew kinds'
providers, and — the list an earlier draft missed — every source a route
condition names for itself. A condition such as `machine.load<80` does
not read the group's provider at all; it reads the named source through
`sourceQuota` into `askSource` (internal/dispatch/quota.go:187;
internal/store/routing.go:660), under `askSource`'s own key, which has no
provider in it (harness_limits.go:139). Warming enumerates the routes the
store holds, parses each gated route's condition, and wants one key per
distinct named source beside the per-group provider keys, so a yard whose
providers answer quickly is gated correctly from its first pass and a yard
whose provider hangs pays for it on the refresher, not on a pass. Warm
wants are one-shot unless a reader renews them: a warmed key nobody reads
is dropped after its first answer, under the same lease. Routes written
after startup are not warmed, and their keys take the cold path below
until their first answer lands. The command
itself is the one `Answerer.Ask` already makes, with `limits.Timeout` and
its process-group kill, from the run's own goroutine; internal/limits
is not changed at all, and its rule that nothing there asks by itself
still holds, because the asking is the dispatcher's, as it is today, only
moved.

Ownership is the one new lock. `limitsAsked` gains a mutex that guards the
map and nothing else: the read takes it to copy out the entry, the refresher
takes it to store an answer, and the command runs outside it, so a 30-second
run holds no lock and blocks no read. The wanted set lives under the same
mutex. Everything else about the memory is as today: an answer is stored
with when it was asked, a failure is stored as a failure and reads back as
unknown for its age, and a stale report past its age reads as no fresh
answer rather than gating on a window whose reset may already have passed.
In particular nothing gates on a last-good report beyond its freshness.
Fail-open survives the move for route reads and, at a reset, for a fresh
remembered failure and for a resolution failure (`NoSource`, a failed
`Store.Sources` read, an unreadable `env_file`: nothing to refresh, above);
a reset read with no fresh answer at all is the one
place the proposal does not fail open, as the next paragraphs state
(Open Question 2 puts all three readers' cold cases to the decider).

A resetting agent is not a route, and the proposal treats its cold read
differently. Today `windowOpen` (internal/dispatch/harness_reset.go:123)
waits for the command at the wake: a fresh report that still says a shared
window is used up, with a reset still to come, holds the prompt and
`harnessReset` (harness_reset.go:179, and `crewReset` at :346 for a crew
session) raises the harness error again with the later time; a fresh
failure fails open and the prompt is typed, because a provider that gives
no answer does not hold back a reset whose time has come. Under the cache
a fresh report keeps both meanings word for word, through the same
`UsedUpForStop` reading (no window used up, a window for one model alone,
or a reset already passed each prompt; anything else holds), and a fresh
remembered failure prompts exactly as a fresh failure does today. A
resolution failure prompts the same way, at once and with no defer: where
no source names the kind or provider, where `Answerers` cannot read
`Store.Sources`, or where a session's `env_file` cannot be read, there is
no command to wait for and no run in flight, so treating it as no fresh
answer would leave the wake standing forever (or until a person happened
to declare a source) while spending nothing and deciding nothing — today
`windowOpen` fails open on exactly that error (harness_reset.go:125, and
`sessionWindowOpen` on an unreadable `env_file` at :137), for a session
through `harnessReset` and for a crew member through `crewReset`
(harness_reset.go:346) alike, and the proposal keeps that word for word:
the prompt is typed, one retry is spent, and if the window was in fact
still shut the agent stops again and is told of it then, as today. No fresh
answer — never asked, or stale with the refresh running — does neither: the
wake defers. The session is not prompted, no one of its `HarnessRetries`
(three in its life, internal/dispatch/harness.go:110) is spent, the error
is not raised again yet, and the handle keeps its `harnessResetAt` so the
next pass reads the key again once the refresher has landed. The defer is
bounded by the one refresh it waits for: that run is already running or is
started by this read's want, never queued behind another key's run (the
concurrency rule above), so it lands within
`limits.Timeout` (30 seconds) of its start as either a fresh report, which
then holds or prompts as above, or a fresh remembered failure, which
prompts. An
agent that has already waited hours for its window therefore waits at most
one run longer, and never spends a scarce retry learning that the window
is still shut. `windowOpen` so gains a third outcome beside open and shut
— not known yet — and its two callers leave the wake standing on it; that
is the whole of the new machinery on this path, and bead 1 in section 6
pins each outcome.

A stopped agent's decoration takes the loss instead, and it is stated
rather than repaired. `harnessLimits` (internal/dispatch/
harness_limits.go:183), reached from `harnessError` for a session and from
the crew error path for a member, on no fresh answer writes the
`session_harness_error` it writes on a failure today, with
`limits_unknown` and no `window`, `resets_at` or `wakes_at`: `resetWake`
has no window to wait for, so no reset wake is kept on the handle, the
group starts no hold for that agent's model (`holdBack` reads the same
wait, internal/dispatch/stall.go:164), and the session's idle spell counts
as any idle spell does instead of being held for a reset. The answer the
refresher lands seconds later cannot amend that stored event, and no
second event is written for it; the agent sits as an agent whose provider
gave no answer sits today, until a person prompts it or the idle limit
ends it, and only a later stop — after such a prompt, on a key the first
stop's read has since warmed and leased — is decorated with its window
and wake. The read still wants the key, so that later stop is the common
case and the cold one is a daemon's first stop on a key; bead 1 pins the
loss so it is chosen, not discovered.

The route cost is named plainly, and it is larger than one session. The first
read of a key that has never been asked since the daemon started, before
its warm-up run has landed, reads as unknown, and `Route.Choose` treats an
unknown as passable and selects the group
(internal/store/routing.go:858). Every read until that first answer lands
takes the same path, so every ready bead routed to a group sharing the
key — several groups can share one provider under one environment, and so
share one key — may be claimed and started in that window, not just one
session. What bounds the exposure is not the cache: it is the bounds
that already bound any start. Each group's `Limit`, as `Claim` counts it
in its own transaction, bounds every group. The yard's `max_sessions`
bounds only the groups beside it: `route` applies that cap only where
`store.RunsHere` says the runner runs here (internal/dispatch/
dispatch.go:1323; internal/store/routing.go:2071), so a conditioned
manual or peer group is not bounded by `max_sessions` at all — its group
`Limit` alone bounds its cold claims, and a person or a peer yard, not
this machine's session count, does that work. Beside those bounds is the
window's
length, which runs from the daemon's start, or from a new route's first
read, until the refresher's run for that key lands — one command run,
itself bounded by `limits.Timeout` (30 seconds). Each such claim carries
`limits_unknown` on its claimed event, so the exposure is visible after
the fact, bead by bead. A group whose window was in fact used up can
therefore fill its slots in the daemon's first seconds where today the
first pass would have waited out the command and gated it. The warm-up
shrinks that window to one concurrent run at startup for the keys it
names; it does not close it, and keys it cannot name — a route written
after startup, a source first named by one — open the same window when
they are first read. Open Question 2 puts fail-open against its
alternatives with this exposure stated; the benefit it is weighed
against is the finding answered: a provider that hangs for its full
`Timeout` now costs the refresher 30 seconds and every other key nothing,
and costs the pass only its answerer resolution and memory read (a
`Store.Sources` read, an `env_file` read for a provider group, and the
`limitsAsked` lookup), forever, however often beads become ready. A
provider that hangs every time is retried at its readers' age
while it stays wanted, no faster; the refresher adds no retry of its own.

**Amended (yardr-qv85, 27 Oct 2026): a route keeps a window last known used
up.** A route read with no fresh answer, or with a fresh failure, no longer
forgets a window the provider said was used up until a reset still ahead:
that stays true however old the answer is, where a percentage below 100 does
not. The cache keeps the report before a failed run, and the route hands
`Route.Choose` those windows beside the unknown (`Quota.LastKnown`). They fail
the comparisons they answer, a `/duration` grace included, until the reset or
the next answer that lands, and the reason says "last known: used up until
16:06". The cold window above stays as it was for a key never answered, which
has nothing to remember, and for a daemon that has just started, whose cache
is memory only.

### 3.3. `settleSent` behind a settler in the store

`Store.write` keeps the first half of what it does today and drops the
second. After `db.Batch` commits it still nudges the pump and pings the hub,
and it still collects `w.settle` and `w.queued` from the committed run; there
the similarity ends. The per-bead loop with its 30-second contexts comes
out. In its place the write appends the settled beads to a list the store
guards and nudges the settler, exactly as writers nudge the pump today
(store.go:331: "a nudge is already pending; the pump reads everything new").
A write that queued no bead nudges nothing, and `SettlePeerBeads` over the
socket gains no new traffic.

The settler is the store's second background goroutine, on the pump's own
pattern: started in `Open` beside the pump, stopped by `Close` through the
same `done` channel's discipline, holding nothing that is not re-readable
from the store. It wakes on a nudge, reads what is owed (`EndedAt` set,
`Settled` unset, as `SettlePeerBeads` reads it), and settles one bead at a
time, each under the same `settleTimeout` of 30 seconds the write gives it
today, in `settleSent` itself, which does not change. Serial settles are
the honest default within the settler: a burst of returns drains one by
one there. Serial does not mean alone, because the daemon's pass sweep
stays (below) and can settle the same bead at the same moment from the
pass goroutine. That race is already safe where it matters and wasteful
only where it does not: `settleSent` re-checks its pending record inside
its own write (internal/store/peersend.go:877), so a bead nudged twice, or
nudged and swept at once, is moved on once; but each racer has already
done the `repoFor` git reads outside that write (peersend.go:868, cached
by base commit in `RepoFlows`), so the re-check prevents a duplicate move,
not duplicate reads. A bead whose repository does not read fails as it fails
today, stays unsettled, and is retried by the next nudge and by the daemon's
pass sweep, which stays exactly where it is at the head of `peers`; the
settler's errors are returned to nobody, as the write's are today (`_ =`),
but they are no longer paid for by the writer either. The next write of any
kind does not retry it; the next settle nudge or pass does, which in a yard
with a daemon is the next wake, and in a yard without one is the next bead
whose stay ends.

Close is the case to state. `Close` stops the settler as it stops the pump;
a bead queued and not yet settled stays unsettled in the file, and the next
`Open`'s settler (started with a first sweep, as the daemon's first pass
sweeps today) or the next nudge settles it. Nothing is queued only in
memory: the nudge list can be dropped on close without loss, because what is
owed is a query over `bPeerSent`, not the list. That is the property that
makes the store, not the daemon, the right owner; that ownership is part
of the proposal, not an open question, because the write path already
settles for every caller the store has and the pump precedent sits in the
same file. The writes that end a stay
arrive at the process that owns the file (a peer's return served over the
socket, a fetch recorded by the daemon through its client at
internal/cli/ops.go:146), while the settler belongs with the bbolt file; a
sweep that lived only in the dispatcher would leave a server running without
a daemon never settling, where today its writes settle. Living in the store, it
settles for every caller the store has, and `SettlePeerBeads` keeps its API
place, its `Admin` access (internal/rpc/access.go:411) and its daemon sweep
as the backstop of last resort.

The latency change is one wake. Today a `PeerReceive` answer implies the
bead has already moved; after, it implies the stay has ended and the move
follows within the settler's next drain, concurrent with whatever the
writer does next. The events keep their order per bead: `peer_bead_returned`
is written by the write that ended the stay, and the move `settleSent`
writes comes after it, as it does when the daemon's sweep writes it today.
What disappears is the caller-visible 30 seconds: the socket call returns at
the commit, and a depot whose git hangs costs the settler one 30-second
bound, not every writer behind it.

### 3.4. What a person types and sees

No command, flag or setting changes. What changes is timing, and in three
cold and stale limits cases the words themselves, each in the shape that
case already has today for a provider that gives no answer; every surface
says so in the words it already has:

- `yardr session list` shows a claimed bead's session as `starting` for the
  length of its start, seconds on a healthy yard, rather than for an
  instant. Its `StartedAt` is the claim's time, so the age of a `starting`
  session is how long its runtime has had. A group whose worker is busy
  shows its next bead still dispatchable, not `starting`: nothing was
  claimed for it yet.
- A bead held by a used-up window shows the same one `quota_wait` event with
  its `resets_at` as today, now written from a cached answer up to
  `routeLimitsMemory` old, as it already may be today. A daemon in its first
  seconds, before a key's first answer lands, may claim every ready bead
  routed to the groups sharing that key, each with `limits_unknown` on its
  claimed event, where a warmed-up daemon would have waited; the events say
  which happened, bead by bead.
- An agent that stops on a usage limit in that same cold window shows a
  `session_harness_error` with `limits_unknown` and no window, reset or
  wake, where a warmed-up daemon would have named the window it waits for;
  it is not prompted at a reset and its group holds no session back for its
  model, until a later stop on the warmed key is told with its window. An
  agent already waiting whose wake reads cold or stale is prompted neither
  early nor late by a guess: its prompt waits for the one refresh already
  running (seconds, against a wait of hours) and is then typed, or held
  with the error raised again at the later reset, exactly as a fresh answer
  decides today. A provider that was asked and gave no answer still prompts
  at once, as it does today, and so does a wake for a kind or provider no
  source answers for, or whose session's `env_file` cannot be read: there
  is no command whose answer it could be waiting for.
- `yardr bead show` on a bead at a peer shows `peer_bead_returned` and then
  its move a moment later, instead of both by the time the returning call
  answered. Nothing else about the return, its notes, its hold on a stage
  that does not name the outcome, or its stale case changes.
- `yardr limits` is untouched: it asks its source then and there, as it does
  today, and does not read the dispatcher's cache. The two can disagree for
  up to the cache's age, as a pass's answer and a person's can today.
- The daemon's log gains nothing new to read. `dispatching` is still logged
  at the claim; the start's outcome still logs where it did. A pass that
  used to take minutes now takes its store reads and its remaining serial
  steps (section 2.6), and `stampPass` still stamps it.

### 3.5. What is kept, what is replaced

Kept, word for word: `Route.Choose`, `Route.Gated`, the implicit
under-100 comparison and fail-open (yardr-7q9z); `Claim`'s in-transaction
limit and `Session.live` over `starting` and `running`; `reap`'s
five-minute `start interrupted` rule for a `starting` session no live
worker owns; `runtimeDown`'s doubling outage and `waitReason`'s reading of
it, applied before any later claim for the group; the runners and every
timeout inside them, with no aggregate bound added over them;
`limits.Timeout` and the rule that the limits package never asks by itself;
`settleSent`, `SettlePeerBeads`, and `settleTimeout` as its per-bead bound;
the dispatcher as the only owner of its pass maps; and, on a fresh report,
a fresh remembered failure or a resolution failure (`NoSource`, an
unreadable `env_file`, a failed `Store.Sources` read), `windowOpen`'s
prompt-or-hold decision and `harnessLimits`' window decoration. Not kept,
by the statement in section
3.2 rather than by silence: a route read with no fresh answer still fails
open, a reset read with no fresh answer defers instead of prompting, and a
stop read with no fresh answer loses its window, wake and model hold until
a later stop. Replaced: the one line
in `route` that calls `Start` (a hand-over to the group's worker, with no
queue behind it), the crew's inline `StartCrew` call in the follow-on step,
the three synchronous `askLimits` call sites (reads of the kept memory,
with the run moved to the refresher), and the loop at the end of
`Store.write` (a nudge to the store's settler). New: one worker goroutine
per group (and, later, per crew member) with its pass-owned in-flight
record and its mutex-guarded owned-session set, one refresher scheduler
holding leased wants (its runs concurrent across keys), one settler
goroutine in the store, one mutex on
`limitsAsked`, and one completions channel drained by the pass. No stored
record, no socket method, no migration, no setting, and no new dependency:
the client is not regenerated, because `store.API` is not touched.

## 4. Left out

- **Implementation.** Section 6 lists the beads; this document writes no
  code, changes no timeout inside a runner, and touches no flow.
- **The reap's per-session calls.** `reap` still asks every live session's
  runner for its status in sequence, 2 minutes a call worst case, and
  `teardown` still stops and finishes in sequence (section 2.6). Fanning
  those out is a different design: `alive` feeds a dozen per-session maps
  the pass owns, the `PaneCache` already exists to keep one pass from
  re-reading a pane, and the honest serial order of ending, counting and
  tearing down is worth more than the parallelism until it is measured.
  Named here so nobody reads "no pass waits on the outside" into this
  proposal. Bead 5 in section 6 measures the remainder; it does not promise
  to remove it.
- **The other serial source and git calls.** `rules`, `ci`, `issues`,
  `pushes`, `bases`, `fetches`, `peers` and `backups` keep their places, their
  timeouts and their skip-after-failure waits (section 2.6). Each touches a
  handful of items behind a per-source wait, unlike a start, which every
  bead pays; moving them is separate work with separate evidence.
- **Any start queue at all, pooled or per group.** No worker pool, no
  priority among groups, no setting for starts in flight, and no second
  claimed job waiting behind a group's worker: a queued job would start
  before the pass had drained the previous start's failure and recorded
  its outage (section 3.1). One in flight per group, the next bead
  unclaimed, is the smallest shape that answers the finding; Open
  Question 1 records the alternatives the decider may prefer, and nothing
  here forecloses them later.
- **Shortening the herdr, fetch or checkout timeouts.** Every bound in
  section 2.2 stays. The complaint was never that one call may take two
  minutes; it is that its two minutes are everyone's. Faster failure of a
  genuinely slow depot is the operator's runner work, not this design's.
- **A limits answer anywhere else.** `yardr limits` still asks fresh, no
  doctor line reads the dispatcher's cache, and no second provider mechanism
  (a declared HTTPS call beside the command) is proposed. The cache is the
  dispatcher's memory, as it is today; exposing it is a later bead if anyone
  asks.
- **Parallel or pipelined settles.** The settler drains one bead at a time.
  Returns are rare beside claims; a yard returning hundreds of beads at once
  waits seconds and blocks nobody. Serial is within the settler only: the
  kept pass sweep can race it on one bead, paying duplicate `RepoFlows` git
  reads (cached by base commit) for no duplicate move, as section 3.3
  states; pipelining the settler itself would multiply even that.
- **Changing what counts as a failure.** No new failure class is proposed:
  with no aggregate start bound (section 3.1), nothing expires that did not
  expire today, and a start's outcome classes stay the pass's three.
  Whether a chronically slow-but-healthy depot should count against its
  beads is a routing question this design does not open; Open Question 4
  is where an aggregate bound, with its failure class, would be taken.

## 5. Open questions

Each is the operator's to decide, with the options, what each costs, and the
one I would take. Nothing elsewhere in this document decides them; the
proposal above is written so any answer can be taken, and section 6 names
the bead that takes each answer.

1. **How many starts may one group have in flight?** (a) One, and no
   queue, as proposed: a second ready bead of the group stays unclaimed
   until the worker's completion has been drained and any outage it
   recorded applied, and a hung runtime delays only its own group's next
   bead by one start. (b) One in flight and one queued: a burst starts
   sooner by one bead, and the price is the outage order of section 3.1 —
   the queued job can start in the gap between a runtime failure and the
   pass that records it, so the group is tried again before `waitReason`
   can hold it; taking (b) means specifying how the worker re-checks the
   outage before it starts a queued job, machinery (a) does not need.
   (c) A small fixed pool, say four per yard shared across groups, or
   unbounded: starts sooner still, and the price is reasoning about which
   group's wedged runtime holds the pool, a second queue to bound, and
   the `starting` pile-up of section 2.3. I would take (a); the finding
   is that one group stalls the others, and one in flight with no queue
   is the smallest change that ends exactly that while keeping today's
   outage order word for word. Taken by bead 3.
2. **What should a read with no fresh answer do, for each reader?** No
   fresh answer means never asked, or stale with its refresh due or
   running; a fresh remembered failure is a different return and keeps
   today's fail-open meaning everywhere, routes and resets alike. A
   resolution failure is neither: where no source names the kind, provider
   or named source (`NoSource`), `Store.Sources` cannot be read, or a
   session's `env_file` cannot be read, there is no command to refresh, so
   it is not part of this question — every reader keeps today's fail-open
   on it (routes read unknown, stops write `limits_unknown`, session and
   crew resets prompt at once), no want is registered and no wake defers
   waiting for a run that will never start. The exposure differs by
   reader, and section 3.2 states each: a route read
   can claim every ready bead of every group sharing the key, bounded by
   each group's limit and, only for a group whose runner runs here
   (`RunsHere`: not manual, not peer), by `max_sessions` — a conditioned
   manual or peer group is bounded by its group limit alone — each claim
   carrying `limits_unknown`; a reset read (`windowOpen` at a session's or crew
   member's wake) that prompted at once could type into a window still
   used up, spend one of the session's three `HarnessRetries`, stop the
   agent again and raise the error anew; a stop read (`harnessLimits`)
   writes its `session_harness_error` once, with `limits_unknown` and no
   window, reset, wake or model hold, and the refresh that lands after
   cannot amend it. (a) Split, as proposed: routes fail open at once, for
   the length of one command run at startup or at a new key's first read;
   a reset defers until that run lands — no prompt, no retry spent, the
   wake left standing, the run already running or started for its own key
   and never queued behind another key's run, so the defer is one
   `limits.Timeout` at most — and then holds or prompts on the fresh
   answer; a stop takes the `limits_unknown` loss, its read still warming
   the key so
   a later stop is decorated. (b) Fail open everywhere, resets included:
   no defer machinery and no third `windowOpen` outcome, and the price is
   the wasted prompt above whenever a wake lands in a cold or stale
   window whose provider would have said still shut; the agent recovers
   on its next stop, one retry poorer. (c) Fail closed everywhere until a
   key's first answer: no session starts and no reset prompts into an
   unknown, and every daemon start, and every new route or source, holds
   its gated groups and its due wakes until the run lands — the first
   pass can again wait on a provider's 30 seconds, though now only for
   its groups, not the whole pass. (d) Bound the route fail-open (a fixed
   number of claims per key before its first answer, then hold) and take
   either reset policy above: the route exposure is capped by a number
   someone must choose, and the price is a new per-key counter whose
   meaning (per daemon start? per key? reset when?) is new machinery for
   a window of seconds. (e) Judge a stale reset by its last-good report
   (hold if it said shut, prompt if it said open) while routes fail open:
   no defer and no wasted prompt on a stale-shut wake, and the price is
   gating, or prompting, on a window whose reset may already have passed
   — the reading section 3.2 refuses beyond freshness. I would take (a):
   fail-open is the meaning yardr-7q9z already chose for unknown limits at
   a claim, the warm-up — including the sources route conditions name —
   keeps that window to one run for the keys a yard is known to need, and
   a retry is scarcer than a claim, so the one reader that spends an
   irreplaceable thing is the one reader that waits seconds for the
   answer. Taken by bead 1, including which stop loss, if any, it accepts.
3. **How long does a key stay wanted, and how eagerly is it re-asked?**
   (a) Leased wants at the smallest age their readers asked for, as
   proposed: a key is re-asked while readers keep reading it (route keys
   every five minutes, a stopped or resetting agent's every minute while
   that session stands), runs of distinct keys concurrent with single
   flight per key so a deferred reset never queues behind another key,
   drops out one age after its last read with at
   most one further run, and a warmed key nobody reads drops after its
   first answer — so no command runs, and no login is renewed, for a key
   nobody needs. (b) Wanted forever once wanted, at those ages: a yard
   that once stopped an agent runs that key's command every minute for
   the daemon's life, renewing its login each time (section 3.2), for an
   answer nobody reads; simpler to state, and the cost is paid forever.
   (c) Every minute for every wanted key, leased as in (a): simpler to
   explain, up to five times the provider calls on route keys, against
   the limits file's warning that asking is not harmless. (d) No
   refresher at all, asks on read as today but off the pass by handing
   the whole route decision to the background: a larger change than this
   design, in which `chooseGroup` itself leaves the pass. I would take
   (a); it is today's two ages with the run moved and a lifetime stated,
   no new asking rate invented. Taken by bead 1.
4. **What overall bound may one start have?** The derivation is section
   2.3's: the longest healthy `Herdr.Start` path sums well past ten
   minutes of calls each inside its own bound, fallbacks adding more, so
   no bound below that sum distinguishes a wedged start from a slow
   healthy one. The reap no longer arbitrates the choice: under section
   3.1 a `starting` session a live worker owns is counted live at any
   age, and the five minutes judge only unowned sessions, so every
   option below is compatible with restarts. (a) No overall bound, as
   proposed: per-call bounds only, as today; a start whose every call
   answers slowly is never cut, and its group — one group, with its next
   bead unclaimed — waits until a call actually fails or a human looks.
   (b) Four minutes, failing as a runtime failure: a wedged group recovers
   in minutes, and a depot whose healthy fetch-and-checkout chain runs
   longer than that — inside every per-call bound — has its starts
   counted as outages and its beads returned until the depot speeds up.
   (c) Ten minutes: the same trade at a longer fuse; still below the
   longest healthy path's sum, so still a slow-depot policy, not a wedge
   detector. I would take (a); a bound that cannot tell wedged from slow
   is a policy about depots, and this design's claim is only that one
   group's policy should not stall the others. Taken by bead 3.
5. **Do crew starts move in the same landing order, or first?** The proposal
   sequences crew after bead starts (bead 4 after bead 3), because the
   machinery should prove itself on bead claims before the yardmaster's own
   session depends on it. (a) As sequenced. (b) Crew first: the stall a
   person feels, a yardmaster that cannot come up while a fetch hangs, ends
   one bead sooner, and the proof case is the yard's most important session
   rather than its most common one. I would take (a), narrowly; a broken
   worker under the yardmaster leaves nobody able to be told. Taken by
   bead 4's ordering, which the decider may swap with bead 3.

## 6. The beads it would become

In the order they would land. All are in the yardr depot. Each is small
enough that a builder finishes it without asking, given the answers to
section 5; where an answer changes a bead, the bead says which question it
waits on. Tests extend the tests that already drive each path; a new test
function only where none does, per the depot's rules, and each test waits on
the effect, never on wall time.

### 1. Serve route and harness limits from the background-refreshed cache

Goal: `askLimits` and `askSource` become reads that never run the command
(`cachedLimits` under `limitsAsked`'s new mutex, returning a fresh report,
a fresh remembered failure, or no fresh answer, and returning a resolution
failure — `NoSource`, a failed `Store.Sources` read, an unreadable
`env_file` — at once with no want registered); one refresher scheduler
under `Run` starts each leased-wanted key's command at the age its readers
asked for, single flight per key and concurrent across keys so no key's
run queues behind another's, drops a key one age after its last read
(at most one further run), and warms concurrently at start the gated
groups' providers under their groups' environments, the crew kinds'
providers, and one key per distinct source a gated route's condition
names (through `sourceQuota`'s own key). `routeQuota`, `harnessLimits`
and `windowOpen` keep their call sites and ages; on a fresh report, a
fresh remembered failure or a resolution failure their downstream
meanings are today's (`Choose` with fail-open and `limits_unknown`, the
silent no-window report, the
`free` pre-check, `windowOpen`'s prompt-or-hold, `harnessLimits`' window),
and on no fresh answer they take section 3.2's split: routes fail open,
`windowOpen` defers with a third outcome its two callers in
`harness_reset.go` leave standing, `harnessLimits` writes
`limits_unknown` with no wake. internal/limits is unchanged. Takes Open
Questions 2 and 3; if the decider takes another answer to Question 2,
this bead's cold cases change with it and the done-when below is rewritten
to pin that answer instead. Where to look:
internal/dispatch/harness_limits.go (`askLimits`, `askSource`, `fresh`),
internal/dispatch/quota.go (`routeQuota`, `sourceQuota`),
internal/dispatch/harness_reset.go (`windowOpen`, `harnessReset`,
`crewReset`),
internal/dispatch/harness.go (the `harnessLimits` call sites and
`HarnessRetries`),
internal/dispatch/dispatch.go (`Run`, `limitsAsked`), and yardr-7q9z's
merged shape (`Route.Gated`, `Route.Choose`) for what must not move. Done
when: a dispatch test whose limits command hangs past the test's own fuse
completes its pass and logs no command run in the pass, while the
refresher's run is counted once for many ready beads of one provider;
during that first in-flight run, ready beads of two groups sharing the key
are claimed with `limits_unknown` (the cold exposure of section 3.2, pinned
by the test rather than assumed to be one session); a warmed key for a
source a route condition names gates from the first pass exactly as a
warmed provider key does, and a warmed cache that says a window at 100
percent still writes one `quota_wait` with its `resets_at` and starts
nothing, while one at 99.9 percent still starts; a stopped agent's
`session_harness_error` still carries its window from a fresh 60-second
answer, and once that session ends the refresher is counted running its
key no more than once more; a key no reader has read for its age is
dropped and not run again; at a reset wake, a fresh report that still
says the window is used up holds the prompt, spends no retry and raises
the error again with the later reset, and the same wake once the report
says the window has opened types the prompt and spends exactly one retry
— each extended as a case in the reset tests that already drive
`windowOpen` through `harnessReset` and `crewReset`, not as a new test of
the helper alone; a wake whose read has no fresh answer while its refresh
runs types nothing, spends no retry and leaves `harnessResetAt`
standing, and the next pass after the refresh lands decides it as the two
cases above; a wake whose provider was asked and gave no answer within
the age prompts at once, as today; a wake whose kind, provider or named
source no source answers for (`NoSource`), whose `Store.Sources` read
fails, or (for a session) whose `env_file` cannot be read also prompts at
once through both `harnessReset` and `crewReset`, spends exactly one
retry, leaves no want registered and counts no command run — it does not
defer, however long the test waits; a deferred wake whose key becomes due
while another key's command hangs is counted running its own command
without waiting for the hanging one to finish, and decides within its own
run; a stop whose read is cold or stale
writes one `session_harness_error` carrying `limits_unknown` and no
`window`, `resets_at` or `wakes_at`, keeps no reset wake or model hold,
and is not amended or repeated when the refresh lands, while that same
session's next stop on the warmed key carries its window and wake — the
cold case extended into the harness-limits tests that already count
`session_harness_error` events and provider asks; and `go test
./internal/dispatch/ ./internal/limits/` passes with no test having
waited on the hanging command.

### 2. Settle a peer bead's return on the store's background settler

Goal: `Store.write` drops its post-commit `settleSent` loop and nudges a
settler goroutine started in `Open` on the pump's pattern (stopped by
`Close`, sweeping first on start), which drains what is owed
(`SettlePeerBeads`' query) one bead at a time under `settleTimeout`, in the
unchanged `settleSent`; `SettlePeerBeads` keeps its API, its daemon sweep at
the head of `peers`, and its backstop meaning. No socket method, no
regenerated client. The settler's owner is the store, as section 3.3
proposes; no open question. Where to look:
internal/store/store.go (`write`, `Open`, `Close`, the pump),
internal/store/peersend.go (`settleSent`, `SettlePeerBeads`, `endSent`,
`PeerFetched`), internal/dispatch/peers.go. Depends on nothing; it may land
before or after bead 1, and must land before anyone measures the three
together. Done when: a store test that ends a peer bead's stay returns from
its write before a settle made slow on purpose has run, and the bead is then
moved on without any further write; the existing peer-settle tests pass with
their cases extended rather than replaced (outcome applied, stale return,
refused and undelivered, branch awaiting fetch); a `Close` with a bead
queued loses nothing across an `Open`; and `go test ./internal/store/...
./internal/dispatch/` passes, including the socket leg
(`YARDR_TEST_RPC=1`) for the store.

### 3. Start bead sessions on a per-group worker, off the pass

Goal: `route` claims exactly as today and hands the `Job` to one worker per
group (lazy under `Run`, one start in flight and no queue: while a group
has a start in flight the pass claims no further bead for it, and it hands
over again only after draining that start's completion), which calls
`runner.Start` on a context derived from `Run` with no aggregate bound
(per-call bounds only), records its session in the dispatcher's
mutex-guarded owned set for `reap`, and writes the outcome to the store
first (`MarkRunning` or the classified `EndSession`, `SetHandle` for a
partial handle), with `noteHomeStarted` recorded for any handle holding a
home tab before and regardless of whether `MarkRunning` or `SetHandle`
succeeds, as already pass-safe, posting
completions the pass drains at the head of `Reconcile` to run
`runtimeDown`, `runtimeUp` and `forget` before any new claim for that
group. `waitReason` treats a busy worker's group as waiting, in the
`group_wait` shape. `reap` counts an owned `starting` session live at any
age and keeps the five-minute `start interrupted` rule for unowned ones.
No runner changes. Takes Open Questions 1 and 4. Where to look:
internal/dispatch/dispatch.go (`route`, `reap`, `Reconcile`, the `count`
closure), internal/dispatch/homesessions.go (`noteHomeStarted`),
internal/store/routing.go (`Claim`, `MarkRunning`, `EndSession`,
`Session.live`), internal/runner/herdr.go (`Start` and its timeouts) for
what must not move. Depends on bead 1 in landing order only, so the two
stalls are never measured as one; the code does not. Done when: a dispatch
test with one group's runner blocked past its fuse sees another group's bead
claimed and running in the same pass, the blocked group's next bead still
unclaimed with no session in the store, and the first bead's session
`starting` and honestly counted; the same blocked group's first start then
fails as a runtime error, the runner is counted called once, and the second
bead is still unclaimed — it is considered only by a later pass, under the
recorded outage, never by the worker on its own; a blocked, runtime and
plain failure from a worker end their sessions exactly as the pass ends
them today, with the outage recognised on the next pass; a start that
opens a home tab and then has its `MarkRunning` (on success) or its
`SetHandle` (on a partial handle) fail in the store still has that tab in
the home snapshot, in both its finished and in-progress copies, so a
home-loss cleanup over that snapshot still closes it — extended as a case
in the home-snapshot tests that already drive `noteHomeStarted`; a
`starting`
session its worker still owns, aged past five minutes by the test's clock,
is still counted live and not ended, while a daemon restarted over an
unowned `starting` session still fails it as `start interrupted` after
five minutes and teardown closes its recorded handle; a start whose calls
each answer inside their own bounds but which together pass four minutes
is not cut by the worker; the shared herdr runner starts two groups
concurrently without data race under `go test -race
./internal/dispatch/ ./internal/runner/`; and the whole depot gate passes
in an isolated yard with a slow stand-in fetch, where the test command's
own run is the measurement (section 2.6's table updated in the same commit
to say what still waits).

### 4. Start crew sessions on the same workers, keyed by member

Goal: `keepSession` hands a needed crew session to a worker per crew member
(lazy under `Run`, one in flight per member, conversations of one member
serial as today) on the machinery of bead 3; the worker makes the
`CrewStartingFor`, `CrewStartedFor` and partial-handle store writes, and the
pass applies `crewStartFailed`'s counting, the `status` key and `closeLeft`
from the same drained completions. Takes Open Question 5, which may swap
this bead with bead 3. Where to look: internal/dispatch/crew.go
(`keepSession`, `crewStartFailed`, `closeLeft`),
internal/runner/herdr_crew.go (`StartCrew`), internal/dispatch/dispatch.go
(bead 3's worker and drain). Depends on bead 3 for the shared worker,
completions channel and drain. Done when: a crew test with one member's
`StartCrew` blocked sees another member start and a bead route in the same
pass; an early exit still counts as a failed start with its pane's tail on
the journal; a restart-requested session still waits for its handoff grace;
and `go test ./internal/dispatch/` passes, with the crew hand-check (a
yardmaster session starting while a bead group's fetch hangs) recorded from
an isolated yard on the bead.

### 5. Say what a pass still waits on, from the pass itself (the decider's to keep)

This bead is not part of the three moves: it instruments what they leave,
and the decider may take it, drop it, or file it separately without
touching beads 1 to 4. It is listed here so the remainder of section 2.6
has a named next step either way.

Goal: `Reconcile` times its named steps and, when a pass passes a threshold
of one interval, logs one line naming each step's share, so the waits left
in section 2.6 (reap's per-session calls first among them) are measured by
the daemon that suffers them rather than guessed at in the next review.
Where to look: internal/dispatch/dispatch.go (`Reconcile` and its steps),
internal/dispatch/gate.go (`stampPass` for where pass facts are already
kept). Depends on beads 1 to 4 landing, since it measures what they leave.
Done when: a dispatch test that makes one step slow on purpose logs the step
by name with its time and no second line for a fast pass; the log line
carries no bead body, handle or credential; and the follow-on question the
line answers, whether the reap fans out, is filed as a backlog bead with the
first real numbers on it rather than decided here.
