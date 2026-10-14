# A bead's ETA as a range from route-report medians, and a 'late' mark when a stage runs past its usual time

Design yardr-hrah, proposed 2026-10-14. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. A depot is a git repository or a plain directory beads are worked in; a bead is one durable piece of work; a flow is the stages a bead goes through, and a stage is one step of it. A stay is one continuous standing of a bead in one stage, from entry to exit. A route says which group takes the beads of a stage, and a group is who works it: a pool of agents, a script, people, or another yard. A runner is how a group's work starts: `herdr` (an agent), `exec` (a command), `manual` (people), `peer` (another yard). A session is one agent working one bead in one stage. A claim is the dispatcher giving a bead to a group, logged as a `claimed` event; a move between stages is logged as an `advanced` event. A send-back is a move into a stage the bead had already left, from one it reached later. An estimate is what someone says of a bead before it is built: how large and how hard. A cell is one group, estimate label and stage whose durations the yard keeps. The median of a set of durations is the middle one; the p90 is the one nine tenths fall at or under. Footplate is the yard's native app, a window onto the yard; signalbox is a second front end that reads the yard only through the web view's JSON routes and its event stream. See docs/glossary.md for all of yardr's words.

What follows is the smallest version under the decisions in section 5; where a fork lives, the section names it and points at the question.

## 1. The question

mckean, 14 Oct 2026 (mails 29101, 29121): "do we have the concepts of estimates? to have an estimate time of arrival of a bead?" — "can this be computed efficiently? we don't want this to incur heavy load on the yardr process. also if something is stuck it can be labelled as such, just like with trains. like an alert symbol". So: from what the yard already knows about what beads cost, can it say when a bead will arrive, cheaply enough to show everywhere, and can it mark a bead that runs past its usual time the way a railway marks a late train?

## 2. What exists today

### 2.1. A bead has a priority and labels, but no estimate

A bead has a priority (0 is highest, 2 the default: `Priority` and `DefaultPriority` in internal/store/beads.go), a type, labels and metadata. How large and how hard it is are labels by operator convention, `difficulty:N` and `size:S`. Routes match those labels by exact membership: `MatchRoute` (internal/store/routing.go) sends the bead to a route only when the bead carries every label the route asks for. Label prefixes (`difficulty:`, `size:`) are only how the route report groups beads into rows (`hasReportPrefix` in internal/cli/routereport.go), not how routing matches. Design yardr-xf5c (section 2, "A bead has no estimate") records that nothing on a bead says how large or hard it is beyond those labels.

### 2.2. The route report already measures what beads cost, by reading the whole log

`yardr route report` (internal/cli/routereport.go) prints, per group and estimate label, the closed beads a group claimed, the send-backs per bead, the failed sessions, and the median build: from the claim to the move that left the stage. `summarizeRouteEvents` pairs each `claimed` event (which carries group, stage and session) with the `advanced` event that left the stage (which carries from, to and group): each exit ends the latest claim of its bead in its stage that no earlier exit ended, so a claim a failure cut short has no build and the reclaim's does. Only closed beads count, under the labels they have now, or under `-` with none. By default it covers every stage a route sends to a group that starts agents (a herdr group: `reportStages`).

It is a batch read, not a live lookup. `collectRouteReport` reads the whole retained log oldest first (`EventsSince` in pages of 1000) and every bead, and writes nothing. Run 14 Oct in this yard (events since 3 Oct): 1.5 s wall, CLI start included. That cost is why the report is a command a person runs, and why an ETA shown on every page cannot be computed the same way per request.

The same run shows how thin the cells get when two labels cross. `muse-builders difficulty:3` has 11 beads in eleven days; split three ways by size, no cell would clear a 10-sample floor. `yardr-builders difficulty:2` has 50, `muse-builders difficulty:2` 39: per group and one label the cells are usable.

Durations are formatted by `limits.Span` (a span like `2h 5m`) and `limits.Clock` (a time like `14:05`) in internal/limits/limits.go; the report's medians go through `Span`. Its middle is `medianDuration` (internal/cli/routereport.go): the middle duration, or the mean of the two middle ones when there are evenly many.

### 2.3. The dispatcher starts beads by priority, and each group has a limit of sessions

`Store.Dispatchable` (internal/store/beads.go) lists the ready beads no group owns yet, sorted by priority only — a stable sort over whatever order the open-bead index returns, which is not creation time. That order is the queue, but two beads of equal priority stand in it in no promised order, and `ReadyBead` carries no creation time to sort them by. A group runs at most `Limit` sessions at once (`Group` in internal/store/routing.go; the herdr groups of this yard sum to 15), enforced in the claim's own transaction as at least one (`max(g.Limit, 1)` in `Store.Claim`), and the yard as a whole at most `max_sessions` when set (`MaxSessions` in internal/store/settings.go; 0 is no cap), enforced against the sessions that run something on this machine (`RunsHere`: every runner but `manual` and `peer`). A claim is logged by `Claim` (internal/store/routing.go) and a move by `Store.Advance` (internal/store/beads.go), each in the same write as the state it changes.

### 2.4. The future is not a list: flows branch, and routes choose among groups at claim time

A flow (`Flow` in internal/store/flows.go) holds stages in the order they were written, transitions per stage, and outcomes: the named ways work in a stage can end and where each leads. `Flow.Next` lists where a bead in a stage may move to — often more than one — and `FlowMap.Stages` is declaration order while `FlowMap.Path` is only the bead's history (internal/store/flowmap.go): neither says what comes next for this bead. The one conventional way on is the default outcome, `done` (`DefaultOutcome`).

Who works a future stage is likewise not one group. A route names its groups in the order they are tried (`Route.Groups`), the first of which (`Route.Worker`) says who works the stage; the rest (`Route.Further`) are quota fallbacks, and which of them takes a bead is chosen when it is claimed, by the quota then (`chooseGroup` in internal/dispatch/dispatch.go). An estimate for a stage ahead can therefore name the route's first group, never the group that will actually take the bead.

### 2.5. What a stay leaves behind: events, a session row, and the bead row

A stay starts with a `created` event (which carries the starting stage: `createdData` in internal/store/beads.go) or an `advanced` event into the stage, is worked under a claim (a `claimed` event plus a session row carrying group, stage and start time: `Store.Claim` in internal/store/routing.go), and ends with an `advanced` event out. Sessions are indexed live per group (`bSessLive`, which claims and the dispatcher count without reading ended sessions) and per bead in start order (`bSessBead`), and `Store.Sessions` lists the live ones from their index. The bead row knows its creation time, its stage, and the group and session that hold it now, if any. The row does not know when the current stay started or which claim of it is latest: today only the event log says that, newest first (`Store.Events` in internal/store/events.go), and no fixed limit bounds how far back the answer sits.

### 2.6. Late-adjacent marks already exist, and each is told apart

A session whose agent never got going is reported as `session_stalled` once it has sat idle since its first prompt for the group's `stall` limit (default 2m: `DefaultStallLimit` in internal/dispatch/stall.go), with the pane's last lines. A session may also raise `session_idle` (default 30m) and `session_aged` (default 4h: internal/dispatch/limits.go). These are events in the log, and the yardmaster hears of them through subscriptions.

The web view's yard panel (`/flows?panel=1`) tells every blocked kind apart as a light on the track: solid red for held, hollow red for gave up, hollow green for stalled, green with a red ring for blocked on a prompt, blue for waiting on a dependency, a red diamond for a bead back on `failed` or `changes` (docs/web.md, "The yard panel"; `flowdraw/panel.go`). The stalled lamp is computed at render: `stalled` in internal/web/panel.go reads the bead's events for the session's last word. Every panel colour is a named token of the stylesheet, so a new mark is one token and one block class.

What waits for a person is listed apart: the home page's `attention` (held, blocked, stranded, open asks, parked beads, faults first) and the `/line` page — the right column as a page — with the waits and the line in numbers (docs/web.md, "Pages", "The right column"). A bead's page (`/beads/<id>`) is what `yardr prime --bead <id>` knows, for a person, and every page also answers JSON.

### 2.7. One rendering carries the bead's header everywhere text does

`printBeadFlow` in internal/cli/beads.go prints the bead's header: id and title, type, stage, status and priority, depot, flow, family, discovery, blocks, links, owner, the routing line (why it went to its group or what it waits for), and labels. Both `yardr bead show` and `yardr prime --bead` print through it, so one line added there appears in both. `bead show --json` adds `routing` and `notes` beside the bead's own fields under keys the web view reuses.

Footplate reads each bead it shows through the socket's `Bead` call and re-reads it on every event of the bead (docs/api.md). Signalbox reads the web view's JSON routes and the event stream only. Additive fields on those payloads reach both without a new call.

### 2.8. Telling anyone is a subscription on an event kind

The `yardr` source delivers bead events to whoever subscribes, filtered by `--where` keys including `kind` (`created`, `advanced`, `claimed`, `gave_up`, `session_blocked`, `session_stalled`, …), `bead`, `stage`, `group` and `label` (docs/subscriptions.md, "The yardr source"). `yardr bead watch` is sugar for watching one bead. A new event kind is therefore heard by crew members, and through them by people, with no new machinery; display alone needs none of it. What the dispatcher may log is closed: `DispatchKinds` in internal/store/events.go, through `LogDispatch`.

### 2.9. The log is pruned, so anything per-request must not depend on its depth

`YARDR_EVENT_RETENTION` keeps events 90 days by default (docs/configuration.md, "Retention: keeping `yard.db` bounded"). The route report's first line says from when the log still holds events. Anything an ETA is computed from at request time must be bounded input: a small record read by key, not a scan.

## 3. The proposal

The existing mechanism that carries it is the route report's pairing — claim to exit, per group — kept incrementally instead of recomputed per read, plus the stay clocks the bead row needs so the pairing never scans the log. What is new: a small aggregate per group, estimate label and stage, written when a stage ends; one line of arithmetic per bead shown; and a late mark beside the stalled lamp. Nothing scans the log per request; nothing runs per page load beyond sorting at most a few hundred numbers.

### 3.1. Per group, label and stage: two rings of durations and a send-back share

For each group, estimate label and stage the yard keeps two rings of the last 200 stays that ended under a claim: entry-to-exit durations (stage entry to the move out) and claim-to-exit durations (latest claim to the move out), with a count of stays and of the exits that sent the bead back. The median and the p90 of each ring are computed when read, by sorting at most 200 durations: microseconds. The median is the report's (`medianDuration`, moved to the shared package, not copied); the p90 is the duration at index ceiling(0.9n) minus one of the sorted ring. Which labels key the cells is question 2; the cells are written the same way regardless.

The samples are recorded without reading any events. The bead row gains three fields, all written in the same transactions as the events they describe: when the current stay started (set by `CreateBead` and `Store.Advance`), when it was last claimed and by which group (set by `Store.Claim`, cleared when the stage changes), and the stages the bead has reached in first-visit order, capped at 64 (flows are small; the order is what the send-back rule reads). When `Store.Advance` logs the `advanced` event it appends both durations to the claiming group's cell, or only the stay and send-back counts when the stay ran under no claim. A reclaim after a failure overwrites the claim fields, so the exit pairs with the latest claim, as the report's pairing does. A move is a send-back by the report's rule: into a stage the bead had already reached from one it reached later (same-stage moves are not exits and record nothing). The per-stage stretch of section 3.2 is then one plus the cell's send-back share: send-back exits over stays. A restart changes nothing: fields and events commit in one write, and the seed below only runs for yards from before them.

A yard that upgrades seeds the cells by replaying the retained log through the same pairing once — the route report's reader, writing instead of printing — so estimates exist from the first day instead of after weeks of stages ending. Open stays start their clocks at the seed: their stage entry is set to the seed time, their visited lists rebuilt from the replay, their claim fields set from the live session where one holds the bead. The late marks of section 3.3 therefore take up to one p90 to re-arm after the upgrade; the document of bead 5 says so.

No estimate is shown from a cell below 10 samples: the stage is named as unclocked ("review: too new to time"), never silently backed by another cell's number. The ETA line appears when at least one contribution ahead is clocked; a bead with nothing clocked ahead keeps today's header, and the JSON fields stay absent.

### 3.2. A bead's ETA is arithmetic over those cells

For an open bead with estimate label L (question 2 says which labels count):

The stages still ahead are walked from the bead's flow along its default outcomes: from the current stage, follow each stage's `done` outcome (or its single way out where no outcome is named `done`), counting every step, up to one full loop plus one step past the flow's stage count. The walk stops at a terminal stage (arrival), at a stage no flow governs the bead with (no stages part at all), at a fork with no `done` way on (named: "then review or decide"), at a peer stage or a stage with no route (named: "then deploy on yard X", "then review, unrouted"). A stage that waits for a person (a manual stage) contributes no time and the walk continues past it: it is named, unclocked, as "after your decision".

Each counted machine stage — one a herdr or exec group works — contributes its cell's median for the group the route's first group names (`MatchRoute`, then `Worker`: section 2.4 says the quota choice is unknowable before the claim, so the estimate is for the primary group and names it). A claimed bead's current stage contributes the remaining median instead: the claim-to-exit median less the run's age so far, floored at zero. A stage whose cell is below the sample floor is named as unclocked, as section 3.1 says.

The queue wait is counted only for a bead no group holds; a claimed bead's queue is zero. Its model is work over capacity. The work ahead is the medians of the beads ahead of this one in dispatch order — by priority, then creation time, the order `Dispatchable` returns once bead 2 gives its tie-break explicitly — that route to the same group, each under its own label's cell, plus, for each live session of the group, the claim-to-exit median less its age so far, floored at zero: the running work that must end before the queued work starts. The capacity is the group's limit, at least one, as claims enforce it. The wait is the work divided by the capacity; with no work ahead and a free slot it is zero and the line omits the queue part. When the yard sits at `max_sessions` and the bead's group runs here, no new session can start whatever the group's limit says, so the queue is named as unclocked ("behind the yard's cap") instead of numbered. How priority orders the wait is question 3; ahead-in-dispatch-order with running remainders is the version below. A queue with an unclocked bead ahead of it is unclocked too ("behind 4 beads, too new to time"): the wait never mixes clocked medians with guesses.

Each counted stage's contribution is stretched by its own cell's send-back share: multiplied by one plus send-back exits over stays. It is shown as a range: the stretched medians summed for the low end, the stretched p90s summed for the high end, each formatted with `limits.Span`. That range is a component-wise heuristic — sums of per-stage medians and p90s — not a calibrated p90 of the total, and every surface that prints it says "usual", never "by" or "at".

Every input is a record read by key or a listing the store already returns sorted or indexed: the cells, the dispatchable listing, the live sessions, the routes. No log scan, no provider asked, no clock in a people's stage.

### 3.3. Late is past the p90 of the matching clock, beside the stalled lamp, never replacing it

A bead is late when its current clock has run past the matching cell's p90 for its group and label. Which clock is which depends on whether anyone holds the bead. A bead no group holds is waiting-late when its stage age — now less the stay start on its row (section 3.1), which the first stay starts at creation — runs past the cell's entry-to-exit p90. A bead a group holds is running-late when its run age — now less its live session's start — runs past the claim-to-exit p90. Like is compared with like: waiting time against whole stays, run time against claimed runs. Held beads, people's stages, peer stages and stages with no route are never late: a hold, a person, another yard or nowhere to go is already named where the bead stands.

Stalled means the agent stopped — its last word is `session_stalled`; late means it takes long. A bead can be either, both, or neither, and the marks stand side by side.

Whether late also tells anyone — a `bead_late` event once per stay, heard through the `yardr` source like `session_stalled` — or only shows, is question 4. The marks below exist either way.

### 3.4. Where it shows, and what a person types and sees

One line in `yardr bead show`, printed by `printBeadFlow`, so `yardr prime --bead` shows it too:

```text
yardr-9c2k  pick the port
  type task · stage new · open · P2
  depot yardr
  ...
  eta usual 2–5h (queue ~1h behind 3 beads for muse-builders; new ~30m; review ~20m; approved after your decision)
```

And when the bead runs past its usual time, beside it, not instead of it:

```text
  late: waiting in new 6h, past the usual 2h (p90 entry-to-exit for difficulty:2 in muse-builders)
```

Neither line prints when there is nothing to say: below the sample floor, or in a people's stage with no machine stage ahead, the header is as today.

The web view shows the same in four places: the bead's page (under the header, beside the routing sentence); the flow cards and `/beads` rows (a late mark on the card, in `card.html`); the yard panel (a new block class beside `stalled` in `flowdraw/panel.go`, a new `--late` stylesheet token — an amber ring on the light, its title naming the stage age against the p90); and `/line`, where late beads stand as rows of their own under the waits, faults first as today, but counted apart from "N need you", since a late bead needs nothing yet.

The JSON grows additively, so signalbox and footplate read it with the calls they already make: `bead show --json`, the bead page's `BeadPage`, the flows and beads listings, the `/line` rows, and the socket's `Bead` call each gain the same two objects, absent when there is nothing to say:

```json
"eta": {"low": "2h", "high": "5h", "queue": "1h", "ahead": 3,
        "stages": [{"stage": "new", "group": "muse-builders", "median": "30m"},
                   {"stage": "review", "group": "muse-builders", "median": "20m"}],
        "unclocked": ["approved"], "samples": 42},
"late": {"stage": "new", "clock": "waiting", "in_stage": "6h", "p90": "2h"}
```

The `stages` entries name each stage's primary group, since quota fallbacks may take the bead elsewhere; `samples` is the smallest cell count behind the range. The beads listing's objects are what signalbox's departure board reads (yardmaster's note of 14 Oct): no second endpoint.

### 3.5. What it costs the yard process

Per claim and per stage exit: a few field writes in the Bolt write that already logs the event — work proportional to one bead, no event read. Per bead shown: a few keyed reads (its cell, the cells of the stages ahead), the dispatchable listing, the live sessions and the routes, and arithmetic over at most hundreds of numbers. Per page load: the same, times the beads on the page — no log scans, nothing like the report's 1.5 s. The aggregates are bounded storage: two rings of 200 eight-byte durations plus counters per group, label and stage, three small fields per bead row — kilobytes for a yard this size — and pruning the event log changes nothing, since the samples outlive the events they came from. The seed replays the retained log once per upgrade, the report's cost, once.

## 4. Left out

**Predicting people's stages.** How long a person takes to review, decide or answer is not in the events in any usable shape — the log says when they acted, not how long they thought — and a clock on a person reads as a deadline. People's stages are named and unclocked. If the yard ever learns them, it is a second design on top of this one's cells.

**Per-model learning.** A bead may pin a model (`--meta model=`), and groups differ by model, but the cells key on group and label, not model. Per-model cells would split the samples thinner than section 2.2 already warns against, for a yard whose routing is by group.

**Which quota fallback takes the bead.** The estimate names the route's first group; a bead the quota sends elsewhere costs what that group's cell says, not this number. Timing the quota would mean predicting usage windows, which is a design of its own.

**A clock for the yard-wide cap.** When `max_sessions` binds, the queue says so and stops numbering. How long until a session ends yard-wide depends on every group's running work at once; the line names the cap instead of guessing.

**A promised time.** The ETA is a usual range from medians, never "Thursday 14:05". A point time pretends a certainty the inputs do not have; the range says what is known. Countdowns, deadlines and promises are not proposed.

**Anything that runs per request over the log.** The report's whole-log read stays a command a person runs. Every per-request path in this design reads keyed records and sorted listings only; that is the efficiency question's answer, and it is load-bearing, not a preference.

## 5. Open questions

**1. Where do the aggregates live: a store bucket, or memory in the server?**

- *A new bucket in the store* (beside the beads, events and sessions in `allBuckets`, internal/store/store.go). Costs a small migration and the usual store tests; the aggregates survive restarts, every reader (CLI, web view, socket) reads the same bytes through the calls they already make, and the write happens in the transaction that logs the move. The stay clocks of section 3.1 live on the bead row under either option — they are the stay's state, not an aggregate. I would take this: it is the only option whose readers need no new path, and durability is what makes the sample floor reachable.
- *Memory in the server, recomputed from the log at boot and updated as moves land.* Costs no migration, but every restart rescans the log (1.5 s today, growing with retention), a crashed server loses the samples the log has pruned, and the CLI and web view need a new read path for what the store does not hold.

**2. Which labels key the cells: difficulty only, or difficulty crossed with size?**

- *Difficulty only* (the bead's `difficulty:` label, else the `-` row). Costs nothing further; the cells hold: 39 and 50 builds for `difficulty:2` in eleven days. A stage whose cell is below the floor is named as unclocked — there is no fallback to another cell's number. I would take this: the report run in section 2.2 shows the crossed cells going thin now, not in theory.
- *Difficulty crossed with size.* Costs the same code but thinner cells: `difficulty:3` has 11 builds before splitting, so most crossed cells would sit below the floor and the line would be absent where it is wanted most.

**3. How does the queue wait handle priority order?**

- *The beads ahead in dispatch order count, plus the running work* — lower priority number, or the same number and older, among the beads that route to the same group, each under its own cell, with each live session's remaining median beside them, over the group's limit; the yard's cap named, not numbered. Costs the explicit creation-time tie-break in `Dispatchable` that section 2.3 says is missing today, so the order estimated is the order dispatched. I would take this: it is the dispatcher's own order with the running work it omits put back.
- *Only the count shows* ("behind 3 beads for muse-builders"), with no time. Costs the least and is never wrong, but answers the arrival question with a shrug where the medians exist.
- *All waiting beads for the group count, regardless of order.* Costs the same code but overstates the wait of a P0 behind twenty P2s, which is exactly the bead whose ETA matters. Not proposed seriously; listed so it stays out.

**4. Does "late" tell anyone, or only show?**

- *Only shows*, on the four surfaces of section 3.4. Costs nothing beyond the rendering: lateness is recomputed wherever the bead is shown, and there is no once-per-stay state to keep. I would take this first: the mark is the ask ("an alert symbol"), and everything it needs is already read.
- *A `bead_late` event too*, logged once per stay when the bead crosses its p90, heard through the `yardr` source with `--where kind=bead_late` like `session_stalled`. Costs a detector (its own pass over live sessions and waiting beads in the dispatcher — the route loop only sees beads no group holds, so a claimed long-running bead needs a separate scan), once-per-stay memory so it fires once, and the subscription documentation. Take it as the follow-up bead if the mark proves worth hearing about; the display does not depend on it.

## 6. The beads it would become

In the order they would land. Each runs the depot's test command (`sh .yardr/test` in the worktree) and the tests of what it changed; every bug found gets a regression test.

**1. Keep the stays' clocks and the builds per group, label and stage, recorded when a stage ends.**

Goal: the row fields and aggregates of section 3.1 exist and stay current. Where to look: `CreateBead`, `Store.Claim` and `Store.Advance` in internal/store/beads.go and internal/store/routing.go (where the stay fields are set and the samples recorded), `summarizeRouteEvents` in internal/cli/routereport.go (the pairing and send-back rules to share, not copy — move them where both use it), `allBuckets` in internal/store/store.go if question 1 goes bucket. Done when: each claim stamps the stay, each stage exit appends both durations (or counts only, unclaimed) to the claiming group's cell and updates the visited list under its cap; cells cap at 200 samples; a seed pass replays the retained log and starts open stays' clocks; `median`/`p90` read helpers answer a cell; store tests cover the pairing (reclaim after failure, send-back move, unclaimed stay), the ring and visited caps, restart safety, and seeding. Depends on: the decisions of section 5, questions 1 and 2.

**2. Share the estimate arithmetic and show the ETA line and the late line in `yardr bead show` and `prime`, with JSON.**

Goal: section 3.4's two lines, in text and JSON, over one shared arithmetic package. Where to look: a new `internal/eta` package holding the pure `Estimate` function over its inputs (cells, dispatchable order, live sessions, routes, flow) and the shared `eta`/`late` JSON structs, so the web view reuses both instead of reimplementing them; `printBeadFlow` in internal/cli/beads.go (both commands print through it); the default-outcome walk over `Flow.Outcomes`; `MatchRoute` plus `Route.Worker` for each stage's primary group; `Store.Dispatchable` for the queue order, gaining its explicit creation-time tie-break with `CreatedAt` added to `ReadyBead`; `limits.Span` for formatting. Done when: `yardr bead show <id>` prints the `eta` and `late` lines exactly as in section 3.4 and omits each when there is nothing to say; `--json` carries the `eta` and `late` objects; `prime --bead` shows the lines; CLI and `internal/eta` tests cover the path walk (fork, loop, peer stop, unrouted stop), the range math, the queue model (running remainders, zero queue, capped yard), the per-stage sample floor, unclocked people's stages, and both late clocks. Depends on: bead 1.

**3. Show ETA and late in the web view: the bead's page, the cards, the panel and `/line`, with JSON.**

Goal: section 3.4's four surfaces, each also answering JSON, through bead 2's `internal/eta` — not in parallel with it, since the contract it builds against lands there. Where to look: the bead page and listings in internal/web (beads, flows, line handlers), `card.html`, `flowdraw/panel.go` with its block classes and the stylesheet's `--panel-*` tokens, docs/web.md's page table and JSON shapes. Done when: the bead's page shows the ETA sentence and the late mark; cards and `/beads` rows carry the late mark; the panel draws the new ring beside the stalled lamp with a title naming age against p90; `/line` lists late beads under the waits, counted apart from "N need you"; every touched JSON shape carries the section 3.4 objects; web tests cover each surface and the additive JSON. Depends on: bead 2.

**4. Log `bead_late` once per stay, heard through the `yardr` source.**

Goal: question 4's event, only if it goes event. Where to look: a new scan of its own in the dispatcher (internal/dispatch/dispatch.go) over live sessions in machine stages and waiting beads routed to machine groups — beside the route loop, which only enumerates beads no group holds and so never sees a claimed long-running bead; `LogDispatch` in internal/store/events.go with `bead_late` added to `DispatchKinds`; a small once-per-stay bucket keyed by bead, stage and stay start, pruned when the bead leaves the stage; the `kind` row of docs/subscriptions.md for the documentation. Done when: a bead crossing its p90 logs one `bead_late` event per stay; re-entering the stage re-arms it; `yardr subscribe <crew> yardr --where kind=bead_late` hears it; `yardr bead watch` does; tests cover once-per-stay, the re-arm, both clocks, and the unheld/machine-stage gating. Depends on: bead 1; build after beads 2 and 3 have put the mark in front of people.

**5. Document estimates where their readers look.**

Goal: the finished behaviour written down once. Where to look: docs/groups.md (what cells exist per group), docs/line.md (what the queue wait counts), docs/web.md (the panel ring, the `/line` rows, the JSON objects), `yardr bead show --help` and `yardr route report --help` where they touch. Done when: each document says what the ETA counts and what it leaves out (component-wise usual range, primary group, unclocked cap and quota), the per-stage sample floor, the seed's restarted clocks, and what late means beside stalled; no claim the code does not keep. Depends on: beads 1–3 (and 4 if taken).
