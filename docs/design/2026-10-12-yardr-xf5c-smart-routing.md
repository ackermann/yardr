# Smart routing: a route names its groups in order, and a group is passed over while its quota is nearly used

Design yardr-xf5c, proposed 2026-10-12. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

Words used here. A yard is one installation of yardr; a bead is one piece of work; a stage is one step of a bead's flow; a group is who works a stage; a route says which group takes the beads of a stage. A harness is the agent program a group starts (claude, codex, kimi, grok, muse), and its kind is which one. A **provider** is the subscription a harness's usage bills to; a **window** is one of its usage limits (so much in five hours, so much in a week) with how far it is used and when it resets; a **limits source** is a declared source whose file names the operator's command that reports a provider's windows. An **estimate** is what someone says of a bead before it is built: how large and how hard. The **dispatcher** is the part of the daemon that starts sessions. docs/glossary.md has the rest.

## 1. The question

mckean, by mail from airy, 12 Oct 2026 22:05, minutes after the yard got its limits source: "overall I would like to have the concept of smart routing, and the information that could go in there is quota remaining, estimated size of the job, estimated difficulty/importance of the job, and then a model is chosen to build it. we could make use of concepts like jev, or also ideas that you have." The bead puts it as: how does the yard choose which group builds a bead, from what is known about the bead and about the subscriptions, so that the heavy work gets the strong model, the plentiful quota gets used, and nobody stops on a limit in the middle?

What he means by jev is on the bead (the yardmaster's note of 12 Oct 22:08, from his mail 19196, "check it here https://typesafe.ai/"): Jev is TypeSafe AI's small decision model. One call sends a state and typed questions of three kinds: Choice (pick one option of a list), Score (score the state on a rubric) and Noul (is this statement true). Each answer is typed and comes with a confidence that software can act on: act when it is high, hand on when it is not. I did not read the vendor's pages or call the service. This design takes the concept and depends on no detail of the product.

## 2. What exists today

**A route names one group, and the first match is final.** A route (`Route` in `internal/store/routing.go`) has a stage, optional filters for one type, one depot and one label, a priority, and one group. `SortRoutes` orders by priority, then by how many filters are set, then by id; `MatchRoute` returns the first that applies. The dispatcher (`route` in `internal/dispatch/dispatch.go`) takes that one route for each ready bead. When its group is at its limit of sessions, its runtime is down, or it is held because one of its sessions stalled or waits for a window to reset (`d.paused`, `holdBack` in `internal/dispatch/stall.go`), the bead waits. No second route is tried.

**Seven other places read a route's group**, each through `MatchRoute`, and section 3.1 has to answer for every one:

- `stageGroup` in `internal/store/routing.go`: the group a bead answers to in its stage. It is the group that claimed the bead, and only for a bead no group has claimed the route's group. `checkMembers` uses it to refuse a move out of a stage of people by someone who is not a member, and `arrivedWork` in `internal/store/peerbeads.go` to tell a linked yard who has its bead.
- `stageWork` in `internal/store/flowmap.go`: who works a stage (people, agents, a script, another yard), the group's name, and a manual group's members and wait. The flow map in a brief is built from it, and so is what a linked yard is told of the stage its beads arrive in.
- `nextStages` in `internal/dispatch/dispatch.go`: for each outcome a brief offers, the group, its runner and its peer; `nextWhen` in `internal/runner/runner.go` makes the sentence "an agent in `yardr-builders` picks it up" from it.
- `yardr review` (`internal/cli/review.go`), the waits and the feed of the web view (`internal/web/waits.go`, `manualStage` in `internal/web/feed.go`): each asks only whether the group is a manual one, and who its members are. Like `stageGroup`, review and the waits take the bead's own group first when it has one.
- The flow columns of the web view (`internal/web/beads.go`): the group's name for a stage, and whether people work it.

Two facts about groups bear on this. Only a manual group has members (`PutGroup` refuses them on any other runner, `internal/store/routing.go`). And a group that exists can be saved again with another runner.

**A group is one harness kind and one model.** `yardr group list` in this yard: `yardr-builders` (claude, claude-opus-5-5, limit 3), `muse-builders` (muse at reasoning effort max, limit 4), `kimi-builders` (limit 2), `grok-builders` (limit 2), `yardr-reviewers` (codex, limit 3). A bead's `--meta model=<name>` replaces the group's model for that bead (docs/groups.md, "Models").

**The choosing is done by hand, with labels.** `yardr route list` for stage `new` in depot yardr: label `muse` to `muse-builders` at priority 3, label `kimi` to `kimi-builders` and label `claude` to `yardr-builders` at 2, and no label to `yardr-builders` at 0. The yardmaster judges each bead and labels it. mckean's rule for that judgement (yard memory mem-4c5n, 11 Oct): the heaviest work to Opus, heavier work to Muse, then Kimi, easy work to Muse or Grok. His rule for quota (mem-wckm, 9 Oct): look at the limits before a batch, and past about 80% of a provider's week with more than a day to its reset, send that provider only P1 work or what he asks for.

**Quota is known, and the daemon reads it only when a harness has stopped.** `yardr limits` today answers for claude (5h 9%, Weekly 11%, Weekly Fable 5%), kimi (Weekly 61%), grok (Weekly 59%) and codex through the source aiquokka, and says of muse: "not asked: muse, in use here and listed by no source". A report (`Report` in `internal/limits/limits.go`) is a list of windows, each with a label, `used_percent` and `resets_at`. It has no start or length of a window. docs/integrations.md ("Say what a subscription still carries") names the five places the command runs and says "Nothing else asks: not a dispatcher pass that finds nothing stopped on a limit, not a session's start", because the command is slow, may be bounded in how often it answers, and may renew a login as it reads. The daemon keeps an answer 60 seconds (`limitsMemory` in `internal/dispatch/harness_limits.go`). When a harness stops on a usage limit and the window resets within six hours, the session waits and is prompted at the reset (`internal/dispatch/harness_reset.go`).

**Limits by provider is partly built.** Design yardr-t5ts is approved. Its first bead has landed (yardr-368s, commits b767edb and ebfee6e): a herdr group or crew member may say `provider`, and `limits.Resolve` and `limits.AskFor` answer for a group's kind and provider setting. The daemon's `askLimits` still asks by kind; that is t5ts bead 3, not yet landed.

**A bead has no estimate.** It has a priority (0 is highest, 2 the default), a type, labels and metadata (`yardr bead create --meta key=value`). The dispatcher's listing of ready beads (`ReadyBead` in `internal/store/beads.go`) carries stage, type, depot, labels and the pinned model; it sorts by priority and does not pass the priority on.

**What a bead cost is in its events.** Read from `yardr events --bead yardr-368s`: `claimed` carries the group, kind, model, session and stage; `advanced` carries from, to, the outcome and the group. So for each bead the events say which group held it in which stage, for how long, and how often review sent it back. The event log is pruned (docs/configuration.md, "Retention").

**A bead that fails is retried by the same group, then held.** A bead whose agent gives it back to its own stage waits a doubling backoff (`backingOff`); after three sessions that died or failed in a stage the dispatcher holds the bead and logs `gave_up` (`MaxAttempts`). No rule moves it to another group.

**A declared source and an exec group.** A declared source is a service added by one file, with calls a granted group or crew member may make (`yardr call`). An exec group runs the operator's command for each bead of its stage, and its session calls under the group's grants (docs/integrations.md). The source artificialanalysis is declared in this yard with a `models` call; this session's group is not granted it, so I did not read what it answers.

**The machine.** Nothing in yardr reads CPU or load. The only cap on parallel work is each group's limit; the herdr groups of this yard sum to 15 sessions.

## 3. The proposal

Two things are missing, and they are separate. First, a route cannot react to anything outside the bead: quota. Second, nothing on a bead says how large or hard it is. The first needs a change to the route. The second needs no new mechanism: labels and the routes that match them carry it, as they carry the hand choice today.

### 3.1 A route names its groups in order, each with an optional quota condition

A route still names the group that takes its beads. It may add, after `--else`, further groups in order. Each group named may carry a condition on its provider's windows, written after `@`:

```sh
yardr route add --stage new --depot yardr \
  --group 'yardr-builders@Weekly<80,5h<90' --else muse-builders
```

This reads: a bead of this route goes to `yardr-builders` while Claude's window labelled `Weekly` is under 80% used and its `5h` window under 90%; otherwise to `muse-builders`. A condition names windows by the label the limits source reports, so yardr knows nothing about any provider's windows, and Claude's `Weekly Fable` window does not gate a group that runs Opus. `@<80` with no label means every window the report has. A group with no condition always holds.

The dispatcher goes through the route's groups in order and takes the first whose condition holds. Everything it checks today (the group's limit, a runtime that is down, a held model, backoff) then applies to that group exactly as now. In particular a group at its limit of sessions is not passed over: the bead waits for it, as today (section 4 says why).

When no group's condition holds, the bead waits. A route with one conditioned group and no `--else` therefore says "this group or nobody": the heaviest work waits for Opus instead of going to a weaker model.

**Only groups that start agents.** A route that names a further group or carries a condition may name only herdr groups, the ones that start an agent of a harness. `yardr route add` refuses any other (`group decisions is manual: --else and a quota condition are for groups that start agents`), and saving a group with another runner is refused while such a route names it. A quota condition means nothing for a group of people, a script or another yard, and this rule is what keeps the next paragraph short: whichever group of such a route takes a bead, the stage is worked by agents, nobody is a member, and nobody waits to be told.

**One reading of a route, for the dispatcher and for everyone else.** Today the dispatcher and each of the seven readers of section 2 take `r.Group` from the route by itself. With several groups on a route that would give two answers: the dispatcher claims a bead for `muse-builders` while the flow map and the brief still say `yardr-builders`. So the reading moves into one place in the store. `MatchRoute` still finds the route. Beside it, one function gives the route's groups in order, each with its condition (`Route.Groups`), and one function chooses among them from the windows it is handed (`Route.Choose`): the group that takes the bead, and each group passed over with the reason. No reader takes `r.Group` any more. Which of the two a reader uses follows from when it asks:

- *A bead a group has claimed.* The group is on the bead, and `stageGroup`, `yardr review` and the waits already take it from there before they look at any route. That is the chosen group, with nothing to resolve.
- *A bead in its stage that no group has claimed, and the dispatcher.* The dispatcher calls `Route.Choose` with the windows it asked for, and it is the only reader that may ask (the next paragraph). `stageGroup`, `yardr review`, the waits and the feed ask of such a bead only whether a manual group's members must act. A route with one group answers as today; a route with several names only herdr groups, so the answer is the same for each of them: agents, no members. Authorization therefore cannot change with the quota.
- *A stage the bead is not in yet*: the flow map, the outcomes of a brief, the flow columns. Nothing has been chosen, and the quota at the time the bead gets there is not known. These name the route's groups in order instead of one: `StageWork`, `NextStage` and `FlowStage` each carry the further groups beside the first, and the sentences say so: "an agent in `yardr-builders` picks it up, or one in `muse-builders` while the quota of `yardr-builders` is nearly used". What they say of who works the stage is unchanged, by the rule above. Whether they should instead name the one group the quota would pick at that moment is open question 7.

**What is new and what is reused.** New: the list of further groups and the conditions on the route record, the two spellings on `yardr route add`, the two functions above with every reader moved onto them, and one step in the dispatcher. Reused: the route, its filters and priorities; the group's `provider` setting and `limits.Resolve` to find whose windows are meant; `limits.AskFor` to ask; the limits source the operator already declared.

**When the limits command runs.** This adds a sixth place to the five docs/integrations.md lists, and that paragraph is rewritten to say so. The dispatcher asks only for a bead that is ready, matched to a route with a condition, at the moment it would otherwise start. It keeps what a provider said for five minutes for this purpose (the 60 seconds of the stopped-harness path are unchanged). So the command runs at most twelve times an hour for each provider, and not at all while no ready bead meets a condition. A pass that asks holds for at most the command's 30 seconds, as the stopped-harness path does.

**What a person sees.** `yardr route list` prints each route's groups in order with their conditions in the GROUP column. The `claimed` event gains `route` (the route's id) and, when a group was passed over, `passed`: each group with the reason in words. `yardr bead show` prints that as one line:

```text
  routed to muse-builders by route qp3f…: yardr-builders passed over (Claude's Weekly window is 83% used, the route asks under 80)
```

A bead that waits because no condition holds gets one event, `quota_wait`, with the same reasons and the earliest reset among the windows that failed, logged once until the bead starts (the way `unrouted` and `backoff` are reported once today).

**How a person overrules it.** With a label, as today: the routes for the labels `claude`, `muse` and `kimi` sit at a higher priority and carry no condition, so a labelled bead goes where the label says whatever the quota is. A bead's `--meta model=` still replaces the model within the group.

**What stays the operator's.** Which groups exist, which provider a group burns, which windows matter and at what percentage, and the order of groups on each route. yardr holds a comparison of two numbers and the names the operator typed. No pack brings a condition: a route in a pack names one group, as now, and the operator adds conditions in their own yard, since a threshold is about their subscription.

### 3.2 The estimate is two labels, written by whoever shapes the bead

An estimate is a label with a prefix, as a topic is (`topic:routing`): `difficulty:1`, `difficulty:2`, `difficulty:3`, and `size:small`, `size:medium`, `size:large`. yardr gives these names no meaning. The operator writes routes that match them, and the names and levels are the operator's to choose. Importance is the bead's priority, which exists.

An honest scale has few levels, and each level is defined by something that can be checked after the bead is built. I propose three levels each, with this rubric as the starting text:

- `difficulty:1`: the code already has the pattern and the bead follows it. `difficulty:2`: new behaviour inside one mechanism, with choices to make. `difficulty:3`: a wrong shape is costly: the store, the dispatcher, concurrency, a design.
- `size:small`: one session, one package. `size:medium`: one session, several packages or a document. `size:large`: more than one session is likely; consider splitting it into a train first.

Who writes it at first: whoever moves the bead out of `backlog`, which in this yard is the yardmaster or mckean. The rubric goes into the yardmaster's role. This is what the yardmaster does now when it picks `muse` or `kimi`, with the judgement recorded as what the bead is instead of where it should go, so that the route and not the yardmaster decides the group and the quota can change the answer.

For routes to use estimates well, two filters are added to the route, both in the family of the type, depot and label filters it has: `--label` may be given more than once (the bead must have every one), and `--bead-priority <n>` matches beads of priority n or more urgent. With them, this yard's table for stage `new` in depot yardr could read:

```sh
# asked for by label: as today, no condition
yardr route add --stage new --depot yardr --label claude --group yardr-builders --priority 6
# the reserve: P0 and P1 go to Opus whatever the week says
yardr route add --stage new --depot yardr --bead-priority 1 --group yardr-builders --priority 5
# a large, hard bead needs more room in the window
yardr route add --stage new --depot yardr --label difficulty:3 --label size:large \
  --group 'yardr-builders@Weekly<60,5h<50' --priority 4
# the heaviest: Opus or wait
yardr route add --stage new --depot yardr --label difficulty:3 --group 'yardr-builders@Weekly<80,5h<90' --priority 3
yardr route add --stage new --depot yardr --label difficulty:2 --group muse-builders --priority 3
yardr route add --stage new --depot yardr --label difficulty:1 \
  --group 'grok-builders@Weekly<80' --else 'kimi-builders@Weekly<80' --else muse-builders --priority 3
# no estimate: the strong group, and Muse when Claude is nearly used
yardr route add --stage new --depot yardr --group 'yardr-builders@Weekly<80,5h<90' --else muse-builders
```

That table is mem-4c5n and mem-wckm written as routes. A bead with no estimate goes where an unlabelled bead goes today (open question 3).

### 3.3 Typed questions with a confidence: an estimating stage, later

What to take from Jev is that an estimate is an answer to a typed question (a Score on the operator's rubric) with a confidence, and that low confidence hands the question to someone else. The yard already has the mechanisms for this, and none of them is new:

- The one who answers is whoever works a stage. A flow gets a stage `estimate` before `new`, and a route puts a group at it.
- With no answering service configured, that group is people and the yardmaster (a manual group), which is section 3.2 with a stage of its own. Or the flow has no such stage and `backlog` does the job, as proposed above.
- With one configured, the group is an exec group whose command the operator names. The command reads the bead, asks the rubric's questions of whatever answers them, writes the labels and a note with each answer and its confidence, and reports the outcome `done`. Jev, or any service with an HTTP interface, stands behind it as a declared source whose call the group is granted (`yardr call`); a small Claude or a rule with no model stands behind it as the command itself.
- Low confidence is an outcome. The command reports `question` instead of `done`, and the flow sends the bead to a stage of people, as it does for any question. The threshold is in the operator's command.

So the answer to the yardmaster's question is yes: the one who answers is declared by the operator, as a source and an exec group, the rubric is the operator's file, and yardr reads only what it reads of any stage: labels, a note, an outcome. This would ship as a pack (a flow with the stage, the group, a command, the rubric file, a declaration for a decision service). I propose it as a later bead, after the report of section 3.4 shows whether hand estimates predict anything (open question 2).

The choice of the group itself is not put to a model. A Choice among the groups a route names is what the route table already is: it gives the same answer for the same bead and quota, and the `claimed` event can say why in one line. A model is asked what a bead is, and the operator's routes decide where it goes.

### 3.4 Learning from outcomes: a report a person reads

`yardr route report` reads the events and prints, for each group and each estimate label, what the beads cost:

```text
GROUP           LABEL          BEADS  SENT BACK  FAILED  MEDIAN BUILD
yardr-builders  difficulty:3   14     0.4        1       41m
muse-builders   difficulty:2   22     0.9        3       18m
muse-builders   difficulty:3   3      2.3        2       55m
```

Beads: closed beads a group claimed in the stage. Sent back: how often per bead a later stage moved it back to that stage. Failed: sessions of the group for it that failed or died. Median build: from `claimed` to the `advanced` that left the stage. `--prefix` picks the label prefixes, `--stage` the stage (default: every stage a herdr group works), `--since` the period; it reads only what the event log still holds and says from when that is. Nothing changes a route from these numbers. A person reads them and edits the routes or the rubric.

### 3.5 What goes wrong

- **A wrong estimate.** A bead labelled easy that is hard goes to a weak group, and review sends it back. Today the same happens with a wrong `kimi` label, and the cure is the same: someone changes the label and the bead routes again. The report shows the pattern (the `muse-builders difficulty:3` row above). A rule that moves such a bead up by itself is open question 4.
- **A limits source that does not answer.** Muse is listed by no source. A condition on a group whose provider no source answers for, or whose command failed, or that names a window the report lacks, cannot be evaluated. What then is open question 1. In either answer the `claimed` or `quota_wait` event says that the limits were not known and why, and `yardr doctor` notes a route whose condition names a group no source answers for.
- **Two groups on one provider.** Conditions resolve through the group's provider (design yardr-t5ts), and the dispatcher's memory is by provider, so two groups on one subscription read one answer and close together. A route whose `--else` group burns the same provider as its first group falls through to a group that is as used up, unless that group has its own condition; `yardr route add` warns when two groups of one route resolve to one provider and the later one has no condition.
- **A bead that bounces between groups.** Within a stage it cannot: the choice is made once, at the claim, and the session keeps its group until it ends, whatever the window does meanwhile. A bead that review sends back to `new` is routed again, and may now go to another group than the one that built it, because a window crossed its threshold in between. The second builder continues on the same branch with the notes of the first. I propose to accept that and show it: the `claimed` event says which group held the bead in this stage before (`was`). Keeping a bead with its first group regardless of quota would bring back the stop in the middle this design is meant to avoid.
- **A window that fills after the start.** The condition is a margin, not a forecast. Three Opus sessions started at 79% can take the week past 80% and a 5-hour window to 100%. Then the harness stops and the existing path takes over: the event says the window, and a reset within six hours is waited for. The thresholds are the operator's lever: the example above asks for more room before a large bead starts.
- **Staleness.** An answer is up to five minutes old, so a group can start one or two sessions after its window crossed the threshold. That is within the margin a threshold below 100 gives.

### 3.6 The machine

This proposal starts no more sessions at once than today's routes allow. A bead is moved to another group only when the first group's quota condition fails; it then takes a slot of the other group, within that group's limit. A group at its limit is still waited for. The most that can run at once stays the sum of the groups' limits (15 herdr sessions in this yard), which is already possible today and already more than the machine carries. What does change is the mix: with Claude's week nearly used, work that would have queued for three Opus slots runs in Muse's four. Routing does not know about CPU and this design does not teach it; a cap on the yard as a whole is open question 5.

## 4. Left out

- **Passing over a group that is at its limit of sessions.** The bead's idea 2 lists "the group below its limit" as a condition. With it, a busy Opus group would spill its queue to weaker groups every time, which is the opposite of sending heavy work to the strong model, and it is the one change here that would raise how much runs at once on a machine short of CPU. A bead waits for its group, as today.
- **A model that chooses the group**, for the reasons at the end of section 3.3.
- **A forecast of what a bead will burn.** Windows are shared by every session and the crew, and a report gives percentages, not tokens per session; nothing honest can say "this bead takes 6% of the week". The size label with a lower threshold stands in for it.
- **Pacing a window against its length** ("use the week up near its reset, not days before"). A report has when a window resets and not when it began, and its label is the provider's text, so yardr cannot say how far through the week it is. The part of mem-wckm that can be computed, "with more than a day to its reset", is the last bead of section 6 and open question 6.
- **Treating a group held on a usage limit as a failed condition.** A group that has a session waiting for a reset is certainly out of quota, and a route could pass it over without asking. Left out of the first bead: a threshold below 100 closes the group before a harness stops, and the hold is shared with stalled sessions, which say nothing about quota.
- **Price and benchmark scores from artificialanalysis as an input to routing.** Every group of this yard runs on a subscription, where the price of a token is nothing and the quota is everything. Scores do not tell how a model does on this yard's beads; the report of section 3.4 does. The source stays what it is: something the operator or the yardmaster reads when deciding the order of groups. It becomes an input when a group is paid by use, and that is its own design.
- **A threshold on the group instead of on the route** (`yardr group set yardr-builders quota=Weekly<80`). It is one place instead of several, but it cannot give P1 beads or a labelled bead a way past, nor a large bead a lower threshold, without a second setting on the route that undoes it.
- **Ordered levels in yardr** ("difficulty at least 2"). yardr would have to know that a label is a scale. With three levels the operator writes a route for each.
- **Routes that change themselves from the report.** The bead asks for a report first, and so does this design.
- **Conditions in packs**, and any migration: routes without `--else` or `@` behave exactly as today (mem-j0y0).

## 5. Open questions

1. **A condition that cannot be evaluated: does it hold or fail?** The provider has no limits source (Muse), the command failed, or the report lacks the window named. (a) It holds: the group takes the bead, as it would have before this design, and the event says the limits were not known. A broken limits command then removes the gate without stopping the yard. (b) It fails: the group is passed over or the bead waits. Safe for quota, but a failing aiquokka then stops every conditioned route until someone notices, and a condition on a Muse group would never hold. I would take (a), with the `quota_wait` and `claimed` events and doctor saying so.

2. **Who writes the estimate?** (a) Whoever shapes the bead, by the rubric in the yardmaster's role: no code, and it starts tomorrow. (b) An estimating stage worked by an exec group and a decision service, as section 3.3 describes, from the start: every bead is estimated the same way and with a confidence, at the cost of a pack, a service to run or pay for, and a stage every bead passes through. (c) First (a), then (b) once the report shows that estimates predict what beads cost. I would take (c).

3. **A bead with no estimate.** (a) It goes to the strong group, as an unlabelled bead does today, and to Muse when Claude is nearly used: nothing is under-served, and unestimated work spends the scarce quota. (b) It waits in `backlog` until it has one: every bead is estimated, and a bead someone forgot to label does not move. (c) It goes to a cheap group. I would take (a): it is today's behaviour, and the quota condition is what protects the week.

4. **Does a bead that review keeps sending back move up by rule?** Today the same group gets it again, and only sessions that die lead to a hold. (a) No rule: the report shows it, and the yardmaster relabels, as now. (b) A route filter `--sent-back <n>`, matching a bead that was moved back to this stage n times or more, on a higher-priority route naming the stronger group. It needs a count the store keeps on the bead, as it keeps `Failures`. The count only grows, so a bead moves up and never down again. Such a route would carry no quota condition, so that an escalated bead does not fall back to the group that failed it. This differs from the hold in that a stronger group tries before a person is asked; the hold after three dead sessions stays. I would take (a) until the report has a few weeks of numbers, then (b) if beads sent back twice are common.

5. **Does anything cap the yard as a whole?** (a) Nothing new: this design starts no more at once than the groups' limits allow today, and the operator keeps those limits low enough for the machine. (b) A yard setting for the most agent sessions running at once across all groups (`yardr settings set max_sessions 6`), which the dispatcher checks beside the group's limit: one number that matches what the machine carries, whatever the routes do. I would take (a) for this design and file (b) as a bead of its own, since it is worth having with or without smart routing.

6. **Should a window close to its reset stop gating?** mem-wckm applies the 80% only "with more than a day to its reset". (a) No: one threshold, always. Quota left at the reset is lost. (b) A condition may add how close to the reset it stops applying, `Weekly<80/1d`: under 80%, or resetting within a day. It uses only `resets_at`, which the report has. I would take (b) as the last bead, after the gate has run through one weekly reset.

7. **What does a view say of a stage whose route names several groups, before a group has claimed the bead?** This is the flow map, the outcomes in a brief and the flow columns of the web view. (a) All of the route's groups in order, as section 3.1 proposes: true whatever the quota does before the bead gets there, and no reader but the dispatcher needs the windows. The cost is a longer sentence, and a reader who wants to know which group it would be now runs `yardr limits`. (b) The one group the quota would pick now. For that every reader needs the windows, and only the daemon may ask for them, so the daemon would keep each provider's last report in the store (a new bucket, written each time it asks) and `Route.Choose` would read it there for the dispatcher and for the views alike. The views then name one group, the same one the dispatcher would take, and they are wrong as soon as a window crosses a threshold between the brief and the claim; the store gains state that is up to five minutes old and is older still while nothing is ready. I would take (a): a stage a bead has not reached has no chosen group, and saying so is more exact than a guess.

## 6. The beads it would become

In the order they would land. Each runs the depot's test command (`sh .yardr/test`) on its last commit. Beads 1 to 4 are the proposal; 6 and 7 wait for the answers to open questions 2 and 6.

Beads 1, 2, 3 and 7 all change the route record, `yardr route add` and `route list` (`internal/store/routing.go`, `internal/cli`), so they land one after another in that order and no two of them are built at once: 2 depends on 1, 3 on 2, and 7 on 3. Bead 5 adds a command of its own and touches none of those files, so it may be built beside any of them.

**1. A route names further groups with `--else`, and a group is passed over while a window of its provider is past the route's threshold.** Goal: section 3.1 whole, since no part of it is useful without the rest. Where to look: `Route`, `routeEvent`, `AddRoute` in `internal/store/routing.go` (the further groups and each group's condition; `AddRoute` refuses a group that does not exist and a condition that does not parse); `yardr route add` and `route list` in `internal/cli`; `Route.Groups` and `Route.Choose` beside `MatchRoute`, and every reader section 2 lists moved onto them so that none reads `r.Group`: `stageGroup`, `stageWork` and `StageWork` with its sentences (`which`, `same`) in `internal/store/flowmap.go`, `nextStages` and `NextStage` in `internal/dispatch/dispatch.go` with `nextWhen` in `internal/runner/runner.go`, `internal/cli/review.go`, `internal/web/waits.go`, `feed.go` and `FlowStage` in `beads.go`; `PutGroup` (refuse another runner while a route with further groups or a condition names the group); `route` in `internal/dispatch/dispatch.go` (after `Match`, `Route.Choose` with the windows asked for); `askLimits` and `limitsMemory` in `internal/dispatch/harness_limits.go` (the same memory, read with an age of five minutes here; ask through `limits.Resolve` with the group's `kind` and `provider`); the `claimed` event where `Claim` logs it; docs/groups.md, docs/flows.md where routes are described, and the "When it is asked" paragraph of docs/integrations.md. Design yardr-t5ts bead 3 edits `askLimits` too; whichever lands second rebases. Done when: `yardr route add --group 'a@Weekly<80' --else b` stores and lists both groups in order with the condition; `route add` refuses `--else` or a condition with a group that is not a herdr group, and `yardr group add` on an existing group refuses to change the runner of one such a route names; with a stand-in limits command reporting `Weekly` at 85% a ready bead is claimed by `b`, and its `claimed` event carries `route` and `passed` with the reason; for that bead once claimed, `stageGroup` gives `b`; for a bead whose next stage has that route, the brief's outcome line and the flow map name `a` and then `b`, the web view's flow column lists both, and none of them runs the limits command; a route with one manual group gives `yardr review`, the waits and `checkMembers` the answers it gives today; `grep` finds no reader of a route's group outside `Route.Groups` and `Route.Choose`; at 70% it is claimed by `a`; with no `--else` and 85% the bead is not claimed and gets one `quota_wait` event with the earliest reset; a group at its limit is waited for, not passed over; `@<80` reads every window; a condition that cannot be evaluated behaves as open question 1 was answered and the event says the limits were not known; the command runs at most once in five minutes per provider however many beads are ready, and never for a route with no condition; every route without `--else` or `@` dispatches exactly as before and every sentence about it reads as before; tests in `store`, `cli`, `dispatch`, `runner` and `web` hold each of these. Run the whole suite once (`YARDR_LANDING=1 sh .yardr/test`), since the change crosses packages. If open question 7 is answered (b), the bucket of reports and the views reading it are part of this bead. Depends on nothing.

**2. `yardr bead show` says why a bead went where it went, and `yardr route add` and doctor warn of conditions that cannot work.** Goal: the line of section 3.1 for a person, and the two warnings of section 3.5. Where to look: `bead show` in `internal/cli/beads.go` (the newest `claimed` or `quota_wait` event of the bead's stage); `limitsPart` in `internal/cli/limits.go` for the doctor note; `yardr route add` for the warning when two groups of a route resolve to one provider and the later has no condition; `was` on the `claimed` event (the group that last held the bead in this stage, when it is another). Done when: a bead claimed past a passed-over group shows the one line; a waiting bead shows what it waits for and until when; doctor notes a route whose conditioned group no source answers for; the warning prints and the route is still added; tests in `cli` and `dispatch`. Depends on 1.

**3. A route may match several labels and a bead's priority.** Goal: the filters of section 3.2. Where to look: `Route`, `SortRoutes` (each label counts as a filter set) and `MatchRoute` in `internal/store/routing.go`; `ReadyBead` in `internal/store/beads.go`, which must carry the priority to the dispatcher; `yardr route add` (`--label` repeatable, `--bead-priority`), `route list`, `routeEvent`; the pack format for a route's label if it is to take a list (docs/packs.md). Done when: a route with two labels matches only a bead that has both and sorts before a route with one; `--bead-priority 1` matches P0 and P1 beads and no others; every caller of `MatchRoute` gives the same answer for a bead; a route with one label and no priority filter behaves as before; tests in `store` and `cli`. Depends on 2: it changes the same route record and the same commands as 1 and 2, so it is built after them.

**4. The rubric, and this yard's routes.** Goal: estimates exist and routes use them. No code. Where to look: the yardmaster's role in `packs/starter/roles` (a section "Estimating a bead" with the rubric of section 3.2, saying the labels are the operator's convention and what a bead without them does, as open question 3 was answered); docs/groups.md (a section "Routing by estimate and quota" with a worked table like section 3.2's and what the events say). Then, by the yardmaster and not by a builder, in the running yard: the routes of section 3.2 added with thresholds mckean confirms, and mem-4c5n and mem-wckm rewritten to point at the routes. Done when: the role and the docs are on main, and the yardmaster's note on this bead lists the routes it added. Depends on 3.

**5. `yardr route report`.** Goal: section 3.4. Where to look: `yardr events` and how it reads the log (`internal/cli`), the `claimed`, `advanced` and session-end events, `Retention` in docs/configuration.md. Done when: for a yard with recorded events the command prints one row per group and label of the chosen prefixes with beads, sent back, failed and median build as section 3.4 defines them, `--json` prints the same rows, a bead worked by two groups in one stage counts for each, the first line says from when the log holds events, and it changes nothing; tests in `cli` over a store with written events. Depends on nothing in code; it is worth running only after 4.

**6. A pack that estimates: a stage, an exec group and typed questions.** Only if open question 2 is answered (b) or (c), and then after 5 has been read. Goal: section 3.3 as a pack in `packs/`, bound to no service: a flow with the stage `estimate`, an exec group, a script that reads a rubric file of typed questions (score, choice, true or false), asks a command or a declared source's call for the answers as JSON with a confidence each, writes the labels and a note, and reports `done` or, under the rubric's confidence threshold, `question`. A declaration for a decision service is an example beside it, not a dependency. This needs a design pass of its own for the question and answer shapes before it is built. Depends on 4.

**7. A window close to its reset stops gating.** Only if open question 6 is answered (b). Goal: `Weekly<80/1d` holds when the window is under 80% or resets within a day. Where to look: the condition's parser and evaluation from bead 1; a window whose `resets_at` the provider does not say never qualifies by time. Done when: a stand-in at 90% resetting in 20 hours lets the group take the bead and the `claimed` event says so; at 90% resetting in 3 days it does not; tests in `store` and `dispatch`. Depends on 3, for the files it shares with it.

Not listed as beads of this design: the escalation filter of open question 4 and the yard-wide cap of open question 5, each to be filed by the yardmaster if mckean picks it.
