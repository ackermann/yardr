# A limit on rounds, and where a bead goes when it is reached

Design yardr-5dzn, proposed 2026-10-21. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead.

## 1. The question

mckean approved this design on 21 Oct 2026, after comparing yardr's flows
with Gas City's formulas, whose `check` with `max_attempts` bounds a loop.
The bead asks for "a limit on how many times a bead may go round a loop in
its flow (review -> new -> review), and where it goes when the limit is
reached (decide, by default), so a bead that keeps coming back stops costing
builder and reviewer sessions and reaches a person instead." Today nothing
bounds such a loop: the dispatcher's give-up counts sessions that failed,
and a reviewer's `changes` is not a failure, so a bead may go new -> review
-> new without end, and the yardmaster sees it only by reading the line. The
bead names six questions: what is counted and from when; where the limit
lives and how a flow file writes it; what happens at the limit and who
hears; how a person sees the count; the default limits in the starter and
design packs and how they show in the route report; and how this sits with
`MaxAttempts` and `gave_up`.

A word used throughout: a **round** is one move by an outcome that takes a
bead back to a stage it has been in before (review's `changes` back to
`new`, the assembly's `failed` back to `new`, critique's `changes` back to
`draft`). Each round costs at least one more builder session and one more
review.

## 2. What exists today

**Flows and outcomes** (`internal/store/flows.go`).
`Flow.Outcomes map[string][]Outcome` holds, per stage, how work there can
end; `Outcome` is `{Name, To}` and nothing else. `normalizeOutcomes` checks
names and makes every outcome's stage a transition. `Flow.Resolve(stage,
outcome)` returns the stage an outcome leads to, `Flow.OutcomesFrom` lists
them (an implicit `done` for a single way out). `ParseFlow` reads the line
format, where an edge is `<to>` or `<to> on <outcome>`:

    review -> review-security on done | approved on done-safe | new on changes | decide on question

and `Flow.String`/`Flow.Edges` write it back. A flow is stored as JSON in
the store (`PutFlow`) or read from a depot's `.yardr/flows/*.flow` on its
base (repo flows); `docs/flows.md` documents both.

**Reporting an outcome** (`internal/store/beads.go`). `Store.Done` calls
`advance` with `donePick(outcome, safe)`, which resolves the outcome
through the bead's flow and records `outcome` on the event's data.
`advance` checks the bead's expectation and unread notes, adds the note,
then calls `move`, which runs `pick`, checks wagons and the stage group's
members, closes the stay (`exitStay`, which also appends to
`beadRow.Visited`), ends the session and logs one `advanced` event with
`from`, `to`, `outcome` and the group. An answer to a stage ask moves the
bead through the same `donePick`. `AdvanceNote` (`yardr bead advance --to`)
moves by hand through the same `move` with a pick that names no outcome.

**What the bead already remembers.** `beadRow.Visited` is the stages the
bead has reached, in first-visit order, capped at `ETAVisitedCap` (64);
`IsSendBack(visited, from, to)` in `internal/store/eta.go` reads it to tell a
send-back from a move on, for the ETA cells and the route report's SENT
BACK. `beadRow.Failures` counts sessions that died or failed in the current
stage; `changeBead` zeroes it whenever the stage changes, and an unhold
resets it.

**The give-up** (`internal/dispatch/dispatch.go`, near line 1306).
`Dispatcher.MaxAttempts` (default 3) is compared with
`Store.FailureCount(bead, stage)` before a bead is dispatched; at the limit
the dispatcher holds the bead (`SetHold`) and logs `gave_up` with `stage`,
`failures` and a hint to unhold. `gave_up` is in `store.AttentionKinds`
(`internal/store/crew.go`), so every crew member and subscription that hears
`attention` hears it: in both packs the yardmaster does (`hears: { events:
[attention] }`). The web reads it back: `internal/web/waits.go`
`gaveUpText` says "gave up after 3 failures in new" on a held bead.

**Packs.** `internal/pack/pack.go` `Stage.Outcomes []store.Outcome`;
`reader.stage` reads `outcomes:` as a map of outcome name to a stage name
(a string, nothing else). `internal/pack/export.go` writes it back the
same way. `packs/starter/pack.yaml` has `changes: new` on review and
review-security and `failed: new` on approved; `packs/design/pack.yaml`
has `changes: draft` on critique and `failed: draft` on approved. Neither
pack limits anything.

**Who sees outcomes.** The session brief's `## Outcomes` section
(`internal/runner/runner.go` `writeOutcomes`, from
`dispatch.NextOutcome`), `yardr prime`'s OUTCOMES list
(`internal/cli/beads.go`, from `Store.NextStages`), the flow drawing
(`internal/flowdraw/plan.go` labels each loop with its outcomes), and the
ETA (`internal/eta/eta.go`), which follows only the `done` way on
(`doneNext`) and never a send-back.

**The route report** (`internal/cli/routereport.go`, `yardr route
report`). One row per group, originator and estimate label: BEADS, SENT
BACK (per bead), REVIEW FIXED, SEC FIXED, FAILED, MEDIAN BUILD.

**What the log says about rounds today.** From this yard's event log (`yardr
events --kind advanced --since 30d`, which holds events since 3 Oct, 7,386
moves, 1,387 beads that reported an outcome): 222 beads had at least one
round. 148 had one, 65 two, 6 three, 2 four, 1 five. By edge: review's
`changes` sent 21 beads back once and none twice (reviewers here mostly fix
forward); critique's `changes` sent 15 beads back once, 40 twice, 2 three
times and 1 four times; the assembly's `failed` sent 125 back once, 20
twice, 3 three times, 1 four and 1 five times. Review-security sent none
back.

**Gas City, for comparison** (`docs/guides/understanding-formulas.md` at
2fe2752, "Self-Checking Work And Transient Hardening"). It keeps two
constructs apart: `retry` re-runs a step that failed for a boring reason,
`check` with `max_attempts` bounds a loop whose work a script judges; at
exhaustion the step closes failed and blocks what follows. Their #3005, as
the bead quotes it, is that a retry keeps no record of the attempts before
it. Ours must.

## 3. The proposal

The limit is a property of an outcome edge: `review -> new on changes`
may say how many rounds it allows and where the bead goes instead once they
are used. The count lives on the bead, beside `Failures`, and the redirect
happens inside the one write that reports the outcome, so nothing polls
and nothing else moves the bead. No new stage, setting, group or command.

### 3.1 What is counted, and from when

The bead counts **rounds per stage it returns to**: `Rounds["new"]` is how
many times an outcome took it back into `new`. A move counts when it is
made by an outcome (`Done`, or an answer to a stage ask) and its stage is
one the bead has been in before, a same-stage outcome included (the
train's `open -> open on failed`). A move by hand (`yardr bead advance
--to`) never counts: a person who moves a bead has already looked at it.

`Rounds` is its own record of where the bead has been, not a reading of
`Visited`. `Visited` is the ETA's list and keeps only the first 64 stages a
bead reaches (`ETAVisitedCap` in `internal/store/eta.go`; `AppendVisited`
drops later ones without a word), while a flow has no limit on its stages
(`internal/store/flows.go` normalizes any number), so a return to a stage
first reached past the cap would never count. Instead every arrival, the
bead's creation and a move by hand included, gives the stage a key in
`Rounds` at zero if it has none; a move by an outcome into a stage that
already has a key adds one; a reset at the limit sets the value to zero
and keeps the key. A key's presence is what makes a move a return. A bead
created before the build has no keys yet, so a move into a stage without a
key also reads `Visited`, and a stage there counts as visited; that
fallback matters only for beads already open, and for them the cap is the
one they already had.

Counting by the stage returned to, not by the edge, means that review's
`changes`, review-security's `changes` and the assembly's `failed` all add
to one count for `new`: each is one more builder session, which is what the
limit is for. (Open question 1 is whether to count per edge instead.)

The count runs from the bead's creation and is set back to zero for a stage
when the limit fires for it: the redirect hands the bead to a person, and a
person who sends it back to `new` has granted it a fresh allowance by doing
so. A question to `decide` in the middle of the loop does not reset it, so a
bead cannot loop for ever by asking once per round. (Open question 2 offers
the other resets.)

### 3.2 Where the limit lives, and how a flow writes it

On the outcome edge, as two optional fields on `store.Outcome`:

```go
type Outcome struct {
	Name string `json:"name"`
	To   string `json:"to"`
	// Max is how many rounds back to To this outcome allows; 0 is no limit.
	Max int `json:"max,omitempty"`
	// Then is where the outcome sends the bead instead once Max rounds are used.
	Then string `json:"then,omitempty"`
}
```

`max: 3` means the bead may be sent back to `To` three times; the fourth
report of the outcome, with three rounds already counted, goes to `Then`
instead. The limit reads the stage's count, whichever edges made it.

In a pack (`packs/*/pack.yaml`), an outcome's value is either the stage, as
now, or a map:

```yaml
review: { group: reviewers, file: flows/default/review.md,
          outcomes: { done: review-security, done-safe: approved,
                      changes: { to: new, max: 3, then: decide }, question: decide } }
```

In the line format, the edge takes two more words after `on <outcome>`:

    review -> review-security on done | approved on done-safe | new on changes max 3 then decide | decide on question

The JSON form of a stored flow carries `max` and `then` on the outcome.
`normalizeOutcomes` refuses `max` below 1, `max` without `then`, `then`
without `max`, `then` equal to `to`, and `max` on the `done` outcome,
and makes `then` a transition of
the stage as it does an outcome's `to`, so the flow allows the redirected
move and the drawing knows the edge. (Open question 3 is whether `then` may
be left out and mean `decide`.) A flow without limits reads, stores and
writes exactly as today.

The limit is refused on `done` because the ETA walks the `done` way on
(`doneNext` in `internal/eta/eta.go`, which follows a looping done way once
round and one step past) and holds no counts, so a limited done loop would
make it predict a stage the store will not take. Every loop the bead asks
about is a send-back outcome (`changes`, `failed`), never `done`, and a
stage with a single way out has an implicit `done` with no fields to carry
a limit, so the refusal costs no flow in the packs or on this yard.

The limit is not on the stage, because one stage has several ways back
with different costs (a failed landing against a reviewer's changes), and
not on the group, because groups are bound per yard while the loop is the
flow's: the same flow under another depot's groups loops the same way.

### 3.3 What happens at the limit

`donePick` resolves the outcome as now. When the outcome has `Max`, its
`To` is a stage the bead has been in (section 3.1), and `Rounds[To]` is already `Max`,
the pick returns `Then` instead, and in the same write:

- the `advanced` event carries, beside `from`, `to` (now `Then`) and
  `outcome`: `rounds` (the count, 3), `max` (3) and `instead_of` (`new`), so
  the log keeps every round and why the bead went where it did;
- `Rounds[To]` is set back to zero, its key kept;
- a `rounds_reached` event is logged on the bead with `stage` (where the
  outcome was reported), `outcome`, `rounds`, `to` (`new`) and `then`
  (`decide`). It is added to `AttentionKinds`, so whoever hears
  `attention` hears it: in the starter and design packs, the yardmaster.
  The bead also enters `decide`, which the yardmaster already hears
  (`stages: [decide]`) and the `decisions` group works, so the person who
  decides sees it where they see every question.

The bead is not held. `decide` is a stage no agent is routed to, so the
redirect alone stops the sessions; a hold on top would add a second thing
for the person to undo. A flow that wants the bead to wait elsewhere names
that stage as `then`.

The note the reviewer gave with `changes` is attached as usual (the
`advance` adds it before the move), so the person in `decide` reads why the
last round was asked for.

### 3.4 How a person sees the count

The count is served on the bead: `Bead.Rounds map[string]int`
(`json:"rounds,omitempty"`), so `yardr bead show --json`, the socket API
(`docs/api.md`) and footplate read it with nothing else to ask.

- **`yardr bead show` and `yardr prime`**: one line under the stage when a
  limited edge leads back to a stage with a count:

      rounds     new 2 of 3 (the next changes from review goes to decide)

- **The brief and prime's OUTCOMES list** name the redirect where it will
  happen, so a reviewer on the last round knows that `changes` reaches a
  person and can write the note for them:

      - `changes` (to `new`, round 3 of 3): an agent in `builders` picks it up; ...
      - `changes` (to `decide`: the 3 rounds back to `new` are used): a human in `decisions` picks it up; ...

  `dispatch.NextOutcome` and `Store.NextStages` resolve it from the bead's
  count, as `donePick` will.
- **The web**: the bead page shows the same rounds line; a bead in `decide`
  whose last move was a redirect says why in its wait reason, as
  `gaveUpText` does for a give-up: "sent to decide after 3 rounds back to
  new (changes from review)".
- **footplate** reads `rounds` and the `rounds_reached` event through the
  API; showing them on its decision card is a bead in the footplate depot,
  named in section 6 and not designed here.

### 3.5 Default limits, and the route report

| pack | edge | limit |
|---|---|---|
| starter `default` | review `changes` -> new | `max: 3, then: decide` |
| starter `default` | review-security `changes` -> new | `max: 3, then: decide` |
| starter `default` | approved `failed` -> new | `max: 3, then: decide` |
| design `design` | critique `changes` -> draft | `max: 2, then: decide` |
| design `design` | approved `failed` -> draft | `max: 3, then: decide` |

Why these numbers, read against this yard's last 18 days (section 2):

- Code beads: with one count for `new` and `max: 3`, 2 beads of 1,387 would
  have reached `decide` (footplate-r1ky and footplate-v1f6, both on a
  fourth failed landing). With `max: 2`, 6 would, all on a third failed
  landing. Three rounds is past where a builder fixes a review note or a red
  gate, and where repeated landings point at the gate or the bead's shape,
  which is a person's to see.
- Design beads: 40 designs went round critique twice, and two rounds is the
  pattern the design flow is written for (draft, critique, redraft,
  critique). With `max: 2`, 3 beads would have reached `decide` on a third
  `changes` (yardr-rhgm, yardr-zyzp, yardr-bwm2); with `max: 3`, one.
  The person in `decide` already reads every design, so sending one there a
  round early costs little.

The train flow gets no default: its rounds back to `open` come from GitHub's
events through the hook's says table and from rebases, and the yard has
too few trains to choose a number from.

Packs apply on import, so this yard's own flows change only when the
operator says so: `.yardr/flows/default.flow` in the yardr depot (a repo
flow, changed by a commit) and the stored `design.design` (changed by
`yardr pack apply` or `yardr flow set`).

**The route report** gets one column, `AT LIMIT`: for each row, how many of
the row's beads a limit sent to its `then` from a stage the row's group
claimed the bead back into. The `rounds_reached` event's `to` names that
stage, so a builder group whose beads keep reaching a person shows it
beside its SENT BACK. JSON: `at_limit`. The best-builder line stays as it
is; it already ranks by sent back and repaired per bead.

### 3.6 MaxAttempts and gave_up

Kept apart. They count different things in different places and answer
differently:

| | `MaxAttempts` / `gave_up` | round limit / `rounds_reached` |
|---|---|---|
| counts | sessions that died or failed in the current stage | outcomes that sent the bead back |
| set where | the dispatcher, one number for the yard | the flow, per outcome edge |
| reset | on any stage change, and on unhold | when the limit fires |
| at the limit | hold in place | move to `then` |
| means | the session cannot finish (crash, quota, harness) | the sessions finish and disagree |

Merging them would either make a failed session a round (and a crashing
harness would send a bead to `decide`) or make a reviewer's disagreement a
failure (and a bead with changes would be held where nobody reads its
note). Gas City keeps its `retry` and its `check` apart for the same
reason. What they share is how a person meets them: both are attention
kinds the yardmaster hears, and the web says each in one sentence on the
bead.

## 4. Left out

- **A hold at the limit, or a hold and a redirect.** A redirect to a
  stage no agent works stops the sessions on its own, and keeps the bead
  where the person deciding expects it; a hold would also need an unhold.
- **A limit on a stage, or on a group.** Section 3.2 says why: one stage has
  several ways back, and a group is the yard's, not the flow's.
- **Counting stay visits.** A visit count would also count first arrivals
  and moves by hand; a round is narrower and is what costs sessions.
- **A command to reset the count.** The limit's redirect resets it, and a
  person who wants another allowance sends the bead back from `decide`.
- **Limiting the train flow by default** (section 3.5), and anything for
  bundling small beads, templates or run-time fan-out, which the bead puts
  out of scope.
- **Changing the ETA.** `internal/eta` follows only the `done` way on and
  never a send-back, and section 3.2 refuses a limit on `done`, so a
  redirect never lies on the way the ETA walks. `eta.Outcome` stays
  `{Name, To}`. Allowing a limited done loop instead would pass the bead's
  `Rounds` into `eta.Bead` and branch the walk on it, which is more than a
  loop no flow has written is worth.
- **Backfilling counts for beads already open.** A bead open when the build
  lands starts at zero; the log keeps its earlier rounds for a reader, and
  the limit applies from then on.

## 5. Open questions

1. **Count per stage returned to, or per edge?** Per stage (review's
   `changes`, review-security's `changes` and approved's `failed` add to
   one count for `new`) bounds the builder sessions, whatever sent the bead
   back; it costs a little surprise, since a bead that failed to land twice
   has one review round left. Per edge (`review/changes` counts alone)
   reads more plainly in a flow file, but a bead may then go round 3 + 3 + 3
   times before any limit fires. I would take per stage.
2. **When does the count start again?** (a) From creation, reset only when
   the limit fires: no route lookup in the write, and a question mid-loop
   does not reset it. (b) Also whenever the bead enters a stage worked by
   people: closer to "since a person last moved it", but the write has to
   resolve the stage's route, the answer changes when routes do, and a
   bead that asks once a round never reaches the limit. (c) Also on unhold,
   as `Failures` is. I would take (a).
3. **May `then` be left out?** (a) Required whenever `max` is given:
   explicit, and a flow with no `decide` stage cannot be wrong by accident.
   (b) Left out means `decide`, refused when the flow has no such stage:
   shorter pack files, but a stage name becomes special. I would take (a).
4. **The default numbers.** Starter `max: 3` on all three edges back to
   `new` and design `max: 2` on critique's `changes` (section 3.5), or
   `max: 2` on the starter's too (6 beads to `decide` in 18 days instead of
   2), or no defaults in the packs at all, leaving the operator to add them.
   I would take the table in section 3.5.
5. **This yard's own flows.** Whether the build changes
   `.yardr/flows/default.flow` and the stored `design.design` to match the
   packs (bead 5 below), or leaves them to the operator. I would change
   them in bead 5: the yard is the first user of the limit.

## 6. The beads it would become

In the order they would land. Each names what it changes; none overlaps the
next's done-when.

**1. Flow: an outcome may carry a round limit.**
Goal: `store.Outcome` has `Max` and `Then`; flows read, check and write them.
Where: `internal/store/flows.go` (`Outcome`, `normalizeOutcomes`,
`ParseFlow`, `Edges`, a lookup of one outcome by stage and name),
`internal/store/flows_test.go`, `docs/flows.md` (the Outcomes section and
its line-format examples).
Done when: `new on changes max 3 then decide` parses and `String` writes it
back unchanged; JSON round-trips `max` and `then`; `normalizeOutcomes`
refuses `max` < 1, `max` without `then`, `then` without `max`, `then` equal
to `to`, `max` on `done`, and an invalid `then`; `then` becomes a transition of the stage;
a flow with no limits renders exactly as before (the existing table rows
pass unchanged); `docs/flows.md` documents the words. Behaviours: 8.

**2. Store: count rounds per stage.** Depends on 1.
Goal: the bead keeps its own record of the stages it has reached and how
many times an outcome brought it back to each.
Where: `internal/store/beads.go` (`Bead.Rounds`, `beadRow`, `CreateBead`,
`move`), `docs/api.md` (the bead's `rounds`), the store test that drives
`Done` (`TestDone` in `internal/store/outcomes_test.go`).
Done when: a created bead has its first stage's key at zero; every move, by
hand or by outcome, gives a stage it reaches for the first time a key at
zero; an outcome into a stage with a key adds one, a same-stage outcome
too; `yardr bead advance --to` into a stage with a key adds nothing; a bead
sent back to the 65th distinct stage it reached counts the round (that
stage is past `ETAVisitedCap`, so `Visited` alone would miss it); a bead
with no keys, open before the build, counts a return to a stage in its
`Visited`; the count is served on `GetBead` and in listings. Behaviours: 7.

**3. Store: redirect at the limit.** Depends on 2.
Goal: a limited outcome goes to `then` once its stage's rounds are used,
and the log says why.
Where: `internal/store/beads.go` (`donePick`, `move`),
`internal/store/crew.go` (`AttentionKinds`), `internal/store/events.go`
(the kinds list), `docs/api.md` (the `rounds_reached` kind and the
`advanced` event's new fields), `TestDone` in
`internal/store/outcomes_test.go`.
Done when: the limited outcome with `Rounds[to] == Max` moves to `then`;
its `advanced` event carries `rounds`, `max` and `instead_of`; `Rounds[to]`
is zero after it with its key kept; a `rounds_reached` attention event is
logged in the same write; a question to `decide` mid-loop leaves the count;
an answered stage ask counts and redirects as `Done` does; a refused
redirect (members, wagon) leaves count and stage unchanged. Behaviours: 7.

**4. Packs: read, check and export the limit.** Depends on 1.
Goal: a pack's outcome may be `{ to, max, then }`.
Where: `internal/pack/pack.go` (`reader.stage`), `internal/pack/export.go`,
their tests, `docs/packs.md`.
Done when: the map form reads into `store.Outcome` with `Max` and `Then`;
`then` naming no stage of the flow is refused with the stages listed, as
`to` is; an unknown key in the map is refused; export writes the map form
for a limited outcome and the plain form otherwise, and a pack round-trips
through import and export unchanged; `yardr pack check` prints a changed limit
as a change to the flow in its plan. Behaviours: 5.

**5. Default limits in the starter and design packs, and this yard's flows.**
Depends on 3 and 4.
Goal: the limits in section 3.5's table, as open questions 4 and 5 decide.
Where: `packs/starter/pack.yaml`, `packs/design/pack.yaml`, their READMEs
and the comment block above `flows:` in each, `.yardr/flows/default.flow`;
the note to the operator names the `yardr flow set` or
`yardr pack apply` that updates the stored `design.design`.
Done when: both packs check and import with the limits; the pack tests that
pin the starter's and design's flows carry them; the depot's flow file
carries them and `yardr flow show yardr` prints them; the stage files of review, critique
and approved say in one sentence that a further round past the limit goes
to `decide`. Behaviours: 4.

**6. Show the count: bead show, prime, the brief and the web.** Depends on 3.
Goal: a person and an agent see the count and where the next round goes.
Where: `internal/cli/beads.go` (bead show, prime's OUTCOMES),
`internal/store/flows.go` `NextStages`, `internal/dispatch` (`NextOutcome`,
`nextStages`), `internal/runner/runner.go` `writeOutcomes`,
`internal/web/waits.go` and the bead page template.
Done when: bead show and prime print the rounds line when a limited edge
leads back to a stage with a count, and nothing otherwise; prime's
OUTCOMES and the brief's `## Outcomes` name the stage the outcome will
really take the bead to, with the round it is; the web bead page shows the
rounds line; a bead in `decide` after a redirect gets the wait reason
"sent to decide after 3 rounds back to new (changes from review)"; JSON
output carries `rounds` unchanged. Behaviours: 6.

**7. Draw the limit.** Depends on 1, and on 5 for the generated drawings.
Goal: the flow drawing shows a limited loop and its redirect.
Where: `internal/flowdraw/plan.go` and `flowdraw.go`, `internal/flowdraw/gen`,
`docs/flows/*.svg` (regenerated, never edited).
Done when: a limited loop's label reads its outcome and limit ("changes, 3
rounds"); the redirect to `then` is drawn as the edge to that stage with
its own label ("after 3"); a flow without limits draws byte for byte as
before; the SVGs under `docs/flows/` are regenerated. Behaviours: 4.

**8. Route report: AT LIMIT.** Depends on 3.
Goal: the report says which groups' beads reach a person through a limit.
Where: `internal/cli/routereport.go`, its test, the command's help text,
`docs/reports.md`.
Done when: each row counts its beads with a `rounds_reached` whose `to` is
a stage the row's group claimed the bead in; text and JSON (`at_limit`)
carry it; an event older than the window counts nowhere, as the other
columns'; the help explains the column. Behaviours: 4.

**Later, in the footplate depot**: show a bead's `rounds` and a
`rounds_reached` redirect on footplate's decision card. Named only.
