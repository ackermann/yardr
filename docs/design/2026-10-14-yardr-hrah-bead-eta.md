# A bead's ETA as a range from route-report medians, and a 'late' mark when a stage runs past its usual time

Design yardr-hrah, proposed 2026-10-14. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. A depot is a git repository or a plain directory beads are worked in; a bead is one durable piece of work; a flow is the stages a bead goes through, and a stage is one step of it. A route says which group takes the beads of a stage, and a group is who works it: a pool of agents, a script, people, or another yard. A runner is how a group's work starts: `herdr` (an agent), `exec` (a command), `manual` (people), `peer` (another yard). A session is one agent working one bead in one stage. A claim is the dispatcher giving a bead to a group, logged as a `claimed` event; a move between stages is logged as an `advanced` event. A send-back is a move into a stage the bead had already left, from one it reached later. An estimate is what someone says of a bead before it is built: how large and how hard. The median of a set of durations is the middle one; the p90 is the one nine tenths fall under. Footplate is the yard's native app, a window onto the yard; signalbox is a second front end that reads the yard only through the web view's JSON routes and its event stream. See docs/glossary.md for all of yardr's words.

What follows is the smallest version under the decisions in section 5; where a fork lives, the section names it and points at the question.

## 1. The question

mckean, 14 Oct 2026 (mails 29101, 29121): "do we have the concepts of estimates? to have an estimate time of arrival of a bead?" — "can this be computed efficiently? we don't want this to incur heavy load on the yardr process. also if something is stuck it can be labelled as such, just like with trains. like an alert symbol". So: from what the yard already knows about what beads cost, can it say when a bead will arrive, cheaply enough to show everywhere, and can it mark a bead that runs past its usual time the way a railway marks a late train?

## 2. What exists today

### 2.1. A bead has a priority and labels, but no estimate

A bead has a priority (0 is highest, 2 the default: `Priority` and `DefaultPriority` in internal/store/beads.go), a type, labels and metadata. How large and how hard it is are labels by operator convention, `difficulty:N` and `size:S`, matched by routes and reports through label prefixes. Design yardr-xf5c (section 2, "A bead has no estimate") records that nothing on a bead says how large or hard it is beyond those labels.

### 2.2. The route report already measures what beads cost, by reading the whole log

`yardr route report` (internal/cli/routereport.go) prints, per group and estimate label, the closed beads a group claimed, the send-backs per bead, the failed sessions, and the median build: from the claim to the move that left the stage. `summarizeRouteEvents` pairs each `claimed` event (which carries group, stage and session) with the `advanced` event that left the stage (which carries from, to and group): each exit ends the latest claim of its bead in its stage that no earlier exit ended, so a claim a failure cut short has no build and the reclaim's does. Only closed beads count, under the labels they have now, or under `-` with none. By default it covers every stage a route sends to a group that starts agents (a herdr group: `reportStages`).

It is a batch read, not a live lookup. `collectRouteReport` reads the whole retained log oldest first (`EventsSince` in pages of 1000) and every bead, and writes nothing. Run 14 Oct in this yard (events since 3 Oct): 1.5 s wall, CLI start included. That cost is why the report is a command a person runs, and why an ETA shown on every page cannot be computed the same way per request.

The same run shows how thin the cells get when two labels cross. `muse-builders difficulty:3` has 11 beads in eleven days; split three ways by size, no cell would clear a 10-sample floor. `yardr-builders difficulty:2` has 50, `muse-builders difficulty:2` 39: per group and one label the cells are usable.

Durations are formatted by `limits.Span` (a span like `2h 5m`) and `limits.Clock` (a time like `14:05`) in internal/limits/limits.go; the report's medians go through `Span`.

### 2.3. The dispatcher starts beads by priority, and each group has a limit of sessions

`Store.Dispatchable` (internal/store/beads.go) lists the ready beads no group owns yet, sorted by priority, then creation time: that order is the queue. A group runs at most `Limit` sessions at once (`Group` in internal/store/routing.go; the herdr groups of this yard sum to 15), and the yard as a whole at most `max_sessions` when set. A claim is logged by `Claim` (internal/store/routing.go, `w.event(row.UUID, "claimed", ...)`); a move by `Store.Advance` (internal/store/beads.go), which logs `advanced` in the same write. `routingOf` (internal/cli/routewhy.go) shows the precedent for reading one bead's recent past cheaply: the newest `claimed` or `quota_wait` of its stay, from the last 500 events of the bead.

### 2.4. Late-adjacent marks already exist, and each is told apart

A session whose agent never got going is reported as `session_stalled` once it has sat idle since its first prompt for the group's `stall` limit (default 2m: `DefaultStallLimit` in internal/dispatch/stall.go), with the pane's last lines. A session may also raise `session_idle` (default 30m) and `session_aged` (default 4h: internal/dispatch/limits.go). These are events in the log, and the yardmaster hears of them through subscriptions.

The web view's yard panel (`/flows?panel=1`) tells every blocked kind apart as a light on the track: solid red for held, hollow red for gave up, hollow green for stalled, green with a red ring for blocked on a prompt, blue for waiting on a dependency, a red diamond for a bead back on `failed` or `changes` (docs/web.md, "The yard panel"; `flowdraw/panel.go`). The stalled lamp is computed at render: `stalled` in internal/web/panel.go reads the bead's events for the session's last word. Every panel colour is a named token of the stylesheet, so a new mark is one token and one block class.

What waits for a person is listed apart: the home page's `attention` (held, blocked, stranded, open asks, parked beads, faults first) and the `/line` page — the right column as a page — with the waits and the line in numbers (docs/web.md, "Pages", "The right column"). A bead's page (`/beads/<id>`) is what `yardr prime --bead <id>` knows, for a person, and every page also answers JSON.

### 2.5. One rendering carries the bead's header everywhere text does

`printBeadFlow` in internal/cli/beads.go prints the bead's header: id and title, type, stage, status and priority, depot, flow, family, discovery, blocks, links, owner, the routing line (why it went to its group or what it waits for), and labels. Both `yardr bead show` and `yardr prime --bead` print through it, so one line added there appears in both. `bead show --json` adds `routing` and `notes` beside the bead's own fields under keys the web view reuses.

Footplate reads each bead it shows through the socket's `Bead` call and re-reads it on every event of the bead (docs/api.md). Signalbox reads the web view's JSON routes and the event stream only. Additive fields on those payloads reach both without a new call.

### 2.6. Telling anyone is a subscription on an event kind

The `yardr` source delivers bead events to whoever subscribes, filtered by `--where` keys including `kind` (`created`, `advanced`, `claimed`, `gave_up`, `session_blocked`, `session_stalled`, …), `bead`, `stage`, `group` and `label` (docs/subscriptions.md, "The yardr source"). `yardr bead watch` is sugar for watching one bead. A new event kind is therefore heard by crew members, and through them by people, with no new machinery; display alone needs none of it.

### 2.7. The log is pruned, so anything per-request must not depend on its depth

`YARDR_EVENT_RETENTION` keeps events 90 days by default (docs/configuration.md, "Retention: keeping `yard.db` bounded"). The route report's first line says from when the log still holds events. Anything an ETA is computed from at request time must be bounded input: a small record read by key, not a scan.

## 3. The proposal

The existing mechanism that carries it is the route report's pairing — claim to exit, per group — kept incrementally instead of recomputed per read. What is new: a small aggregate per group, estimate label and stage, written when a stage ends; one line of arithmetic per bead shown; and a late mark beside the stalled lamp. Nothing scans the log per request; nothing runs per page load beyond sorting at most a few hundred numbers.

### 3.1. Per group, label and stage: the recent builds and the send-back rate

For each group, estimate label and stage the yard keeps the recent build durations — claim to exit, paired exactly as the route report pairs them (section 2.2) — as a ring of the last 200, with a count of stays and of send-backs. The median and the p90 are computed when read, by sorting the ring: 200 durations sort in microseconds. Which labels key the cells is question 2; the cells are written the same way regardless.

The sample is recorded in the write that logs the `advanced` event (`Store.Advance`): the bead's events say which claim the exit ends (the latest claim in the left stage that no earlier exit ended) and whether the move is a send-back (into a stage the bead had already left). That is one bounded read of one bead's events and one small write per stage exit — the event already fires, and the pairing rule is the route report's, shared, not a second definition of a build.

Where the aggregates live is question 1 (a store bucket, or memory in the server). Either way the shape is the same, and a yard that upgrades seeds them by replaying the retained log through the same pairing once — the route report's reader, writing instead of printing — so estimates exist from the first day instead of after weeks of stages ending.

No estimate is shown below 10 samples for the bead's cell: the line is absent and the JSON fields null, so a new group or label degrades to silence, not to a guess from two builds.

### 3.2. A bead's ETA is arithmetic over those cells

For an open bead with estimate label L (question 2 says which labels count and what backs them up):

- The stages still ahead come from the bead's flow map, which `prime` already builds: what comes after this stage, in order.
- Each remaining machine stage — one a herdr, exec or peer group works — contributes its cell's median for the group the route would send it to (`MatchRoute`, the same match the dispatcher makes). A stage that waits for a person (a manual stage, a hold, a wait on a dependency) contributes no time: it is named, unclocked, as "after your decision".
- The queue wait counts only the beads ahead of this one in dispatch order — by priority, then creation time, the order `Dispatchable` already returns — that route to the same group, sums their medians, and divides by the group's free slots (its limit less its running sessions). How priority orders the wait is question 3; ahead-in-dispatch-order is the version below.
- The sum is stretched by the cell's send-back rate: multiplied by one plus send-backs per bead, the report's `SENT BACK` kept per cell.
- It is shown as a range: the medians summed for the low end, the p90s summed for the high end, each stretched the same way, formatted with `limits.Span`.

Every input is a record read by key or a listing the dispatcher already returns sorted. No log scan, no provider asked, no clock in a people's stage.

### 3.3. Late is past the p90, beside the stalled lamp, never replacing it

A bead is late when it stands in a machine stage, unheld, past its cell's p90 for its group and label, counted from when it entered the stage (its newest `advanced` into this stage). Stalled means the agent stopped — its last word is `session_stalled`; late means it takes long. A bead can be either, both, or neither, and the marks stand side by side.

Whether late also tells anyone — a `bead_late` event once per stay, heard through the `yardr` source like `session_stalled` — or only shows, is question 4. The marks below exist either way.

### 3.4. Where it shows, and what a person types and sees

One line in `yardr bead show`, printed by `printBeadFlow`, so `yardr prime --bead` shows it too:

```text
yardr-9c2k  pick the port
  type task · stage new · open · P2
  depot yardr
  ...
  eta 2–5h (queue ~1h behind 3 beads for muse-builders; new ~30m; review ~20m; approved after your decision)
```

And when the bead runs past its usual time, beside it, not instead of it:

```text
  late: in new 6h, past the usual 2h (p90 for difficulty:2 in muse-builders)
```

Neither line prints when there is nothing to say: below the sample floor, or in a people's stage with no machine stage ahead, the header is as today.

The web view shows the same in four places: the bead's page (under the header, beside the routing sentence); the flow cards and `/beads` rows (a late mark on the card, in `card.html`); the yard panel (a new block class beside `stalled` in `flowdraw/panel.go`, a new `--late` stylesheet token — an amber ring on the light, its title naming the stage age against the p90); and `/line`, where late beads stand as rows of their own under the waits, faults first as today, but counted apart from "N need you", since a late bead needs nothing yet.

The JSON grows additively, so signalbox and footplate read it with the calls they already make: `bead show --json`, the bead page's `BeadPage`, the flows and beads listings, the `/line` rows, and the socket's `Bead` call each gain the same two objects, absent when there is nothing to say:

```json
"eta": {"low": "2h", "high": "5h", "queue": "1h", "ahead": 3,
        "stages": [{"stage": "new", "median": "30m"}, {"stage": "review", "median": "20m"}],
        "unclocked": ["approved"], "samples": 42},
"late": {"stage": "new", "in_stage": "6h", "p90": "2h"}
```

### 3.5. What it costs the yard process

Per stage exit: one bounded read of the leaving bead's events plus one small aggregate write — work proportional to one bead, done where the event is already written. Per bead shown: a few keyed reads (its cell, the cells of the stages ahead, the dispatchable listing the dispatcher keeps sorted) and arithmetic over at most hundreds of numbers. Per page load: the same, times the beads on the page — no log scans, nothing like the report's 1.5 s. The aggregates are bounded storage: 200 eight-byte durations plus counters per group, label and stage — kilobytes for a yard this size — and pruning the event log changes nothing, since the samples outlive the events they came from.

## 4. Left out

**Predicting people's stages.** How long a person takes to review, decide or answer is not in the events in any usable shape — the log says when they acted, not how long they thought — and a clock on a person reads as a deadline. People's stages are named and unclocked. If the yard ever learns them, it is a second design on top of this one's cells.

**Per-model learning.** A bead may pin a model (`--meta model=`), and groups differ by model, but the cells key on group and label, not model. Per-model cells would split the samples thinner than section 2.2 already warns against, for a yard whose routing is by group.

**A promised time.** The ETA is a range from medians, never "Thursday 14:05". A point time pretends a certainty the inputs do not have; the range says what is known. Countdowns, deadlines and promises are not proposed.

**Anything that runs per request over the log.** The report's whole-log read stays a command a person runs. Every per-request path in this design reads keyed records and sorted listings only; that is the efficiency question's answer, and it is load-bearing, not a preference.

## 5. Open questions

**1. Where do the aggregates live: a store bucket, or memory in the server?**

- *A new bucket in the store* (beside the beads, events and sessions in `allBuckets`, internal/store/store.go). Costs a small migration and the usual store tests; the aggregates survive restarts, every reader (CLI, web view, socket) reads the same bytes through the calls they already make, and the write happens in the transaction that logs the move. I would take this: it is the only option whose readers need no new path, and durability is what makes the sample floor reachable.
- *Memory in the server, recomputed from the log at boot and updated as moves land.* Costs no migration, but every restart rescans the log (1.5 s today, growing with retention), a crashed server loses the samples the log has pruned, and the CLI and web view need a new read path for what the store does not hold.

**2. Which labels key the cells: difficulty only, or difficulty crossed with size?**

- *Difficulty only* (the bead's `difficulty:` label, else the `-` row), with fallback to the group's row when the cell is below the sample floor. Costs nothing further; the cells hold: 39 and 50 builds for `difficulty:2` in eleven days. I would take this: the report run in section 2.2 shows the crossed cells going thin now, not in theory.
- *Difficulty crossed with size.* Costs the same code but thinner cells: `difficulty:3` has 11 builds before splitting, so most crossed cells would sit below the floor and the line would be absent where it is wanted most. Fallback chains (crossed, then difficulty, then group) recover the line but report a number whose key the reader did not ask for.

**3. How does the queue wait handle priority order?**

- *Only the beads ahead in dispatch order count* — lower priority number, or the same number and older, among the beads that route to the same group. Costs nothing beyond the listing order that already exists; the wait answers "when does this bead start" the way the dispatcher will actually start it. I would take this: it is the dispatcher's own order, not a second one.
- *All waiting beads for the group count, regardless of order.* Costs the same code but overstates the wait of a P0 behind twenty P2s, which is exactly the bead whose ETA matters.
- *Priority bands with weights.* Costs a model nobody asked for and numbers nobody can check. Not proposed seriously; listed so it stays out.

**4. Does "late" tell anyone, or only show?**

- *Only shows*, on the four surfaces of section 3.4. Costs nothing beyond the rendering: lateness is recomputed wherever the bead is shown, and there is no once-per-stay state to keep. I would take this first: the mark is the ask ("an alert symbol"), and everything it needs is already read.
- *A `bead_late` event too*, logged once per stay when the bead crosses its p90, heard through the `yardr` source with `--where kind=bead_late` like `session_stalled`. Costs a detector (a dispatcher pass comparing stage ages against p90s), once-per-stay memory so it fires once, and the subscription documentation. Take it as the follow-up bead if the mark proves worth hearing about; the display does not depend on it.

## 6. The beads it would become

In the order they would land. Each runs the depot's test command (`sh .yardr/test` in the worktree) and the tests of what it changed; every bug found gets a regression test.

**1. Keep the builds per group, label and stage, recorded when a stage ends.**

Goal: the aggregates of section 3.1 exist and stay current. Where to look: `Store.Advance` in internal/store/beads.go (where `advanced` is logged), `summarizeRouteEvents` in internal/cli/routereport.go (the pairing rule to share, not copy — move it where both use it), `allBuckets` in internal/store/store.go if question 1 goes bucket. Done when: each stage exit appends one sample (duration, stay, send-back) to its cell; cells cap at 200 samples; a seed pass replays the retained log through the same pairing; `median`/`p90` read helpers answer a cell; store tests cover pairing (reclaim after failure, send-back move), the cap, and seeding. Depends on: the decisions of section 5, questions 1 and 2.

**2. Show the ETA line and the late line in `yardr bead show` and `prime`, with JSON.**

Goal: section 3.4's two lines, in text and JSON. Where to look: `printBeadFlow` in internal/cli/beads.go (both commands print through it), `nextStages`/`FlowMap` for the stages ahead, `MatchRoute` for each stage's group, `Store.Dispatchable` for the queue order, `limits.Span` for formatting. The arithmetic lives in one pure function over those inputs, so the web beads reuse it. Done when: `yardr bead show <id>` prints the `eta` and `late` lines exactly as in section 3.4 and omits each when there is nothing to say; `--json` carries the `eta` and `late` objects; `prime --bead` shows the lines; CLI tests cover the range math, the queue division by free slots, the sample floor, unclocked people's stages, and the late threshold. Depends on: bead 1.

**3. Show ETA and late in the web view: the bead's page, the cards, the panel and `/line`, with JSON.**

Goal: section 3.4's four surfaces, each also answering JSON. Where to look: the bead page and listings in internal/web (beads, flows, line handlers), `card.html`, `flowdraw/panel.go` with its block classes and the stylesheet's `--panel-*` tokens, docs/web.md's page table and JSON shapes. Done when: the bead's page shows the ETA sentence and the late mark; cards and `/beads` rows carry the late mark; the panel draws the new ring beside the stalled lamp with a title naming age against p90; `/line` lists late beads under the waits, counted apart from "N need you"; every touched JSON shape carries the section 3.4 objects; web tests cover each surface and the additive JSON. Depends on: bead 1 (and bead 2's arithmetic; the two can build in parallel against its shape).

**4. Log `bead_late` once per stay, heard through the `yardr` source.**

Goal: question 4's event, only if it goes event. Where to look: the dispatcher's pass (internal/dispatch/dispatch.go) for the detector, `LogDispatch` in internal/store/events.go for the logging, the `kind` row of docs/subscriptions.md for the documentation. Done when: a bead crossing its p90 logs one `bead_late` event per stay; re-entering the stage re-arms it; `yardr subscribe <crew> yardr --where kind=bead_late` hears it; `yardr bead watch` does; tests cover once-per-stay, the re-arm, and the unheld/machine-stage gating. Depends on: bead 1; build after beads 2 and 3 have put the mark in front of people.

**5. Document estimates where their readers look.**

Goal: the finished behaviour written down once. Where to look: docs/groups.md (what cells exist per group), docs/line.md (what the queue wait counts), docs/web.md (the panel ring, the `/line` rows, the JSON objects), `yardr bead show --help` and `yardr route report --help` where they touch. Done when: each document says what the ETA counts and what it leaves out, the sample floor, and what late means beside stalled; no claim the code does not keep. Depends on: beads 1–3 (and 4 if taken).
