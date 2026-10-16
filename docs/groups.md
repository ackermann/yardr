# Groups and runners

A group is who works a stage's beads: a pool of agents, a code step,
humans, or another yard. Its runner says how work starts; routes (`yardr route add`) send a
stage's beads to it. What a group's settings are, and where they are kept,
is in [configuration.md](configuration.md).

A group and a route log an event when they appear and when they go, so
whoever follows the yard by its events (`yardr events`) sees its structure
change:

| kind | logged by | data |
|---|---|---|
| `group_set` | `group add`, for a new group and for one it changes, `group set` when it changes something, and a pack's import of a group | `name`, `runner`, `limit`, and `members` when it has any; runner config is omitted |
| `group_removed` | `group rm` | `name`, `routes`: the ids of the routes that went with it (they log no `route_removed`), `released`: how many beads it gave back |
| `route_added` | `route add`, and a pack's import of a route | `id`, `stage`, `group`, `priority`, and the filters it sets: `type`, `depot`, `label`, `bead_priority`; for a route that asks for more than one label `label` is the first and `labels` all of them ([below](#which-beads-a-route-takes)); for a route with further groups or a quota condition also `groups`: each group in order, `name` and its condition `when` ([below](#a-route-with-further-groups-and-quota-conditions)) |
| `route_removed` | `route rm` | the same fields, of the route as it was |

Removing a group or a route that is not there logs nothing. A depot's routes
that go with `yardr depot rm` are named by its `depot_removed`.

## Groups, interactively

`yardr group add` with no name opens a form: pick an existing group to edit or
create one, choose the runner, and for herdr the agent (Claude Code, Codex,
Grok, Kimi, opencode, pi, or any other herdr agent), a model read live from
that agent (press `/` to search long lists), and its permission flags. With a
name, `yardr group add <name> --runner … --set …` stays scriptable.

## Changing a group

`yardr group add <name>` writes the group whole: what is not retyped is
gone. `yardr group set <name> key=value …` changes the settings named and
keeps the rest, in one write, and prints each as it was and as it is:

```sh
yardr group set webapp/builders model=opus --limit 3
yardr group set webapp/builders --unset idle
yardr group set approvers --members mckean,yardmaster   # replaces the list
```

A herdr group may say which subscription its agent burns. `provider` names
it as a source's limits command takes it, exactly as a kinds table's values
do, and wins over the kinds table's mapping of the group's kind for that
group; a crew member's config takes the same key. Unset, the kind maps as
before. The name is unchecked when set, as `model` is: one no source
answers for gets no answer, and `yardr doctor` says so
([integrations.md](integrations.md#say-what-a-subscription-still-carries)):

```sh
yardr group set builders provider=zai
```

A config key is one its runner reads, and its value what the reader takes:
the store refuses any other with the keys the group's runner has, whether
the group is written by `group add`, `group set`, the form or a pack's
import, so `idel=5m` or `max-age=6h` is told where it is typed instead of
being stored and never read. A crew member's config is held to the same
([crew.md](crew.md)). A yard set up before the store checked may hold such a
key: it runs as it did, a write that leaves the key as it is goes through,
and [`yardr doctor`](doctor.md#the-yard) notes it with how it is removed
(`--unset <key>`).

The runner is not changed this way, since another runner reads another
config: that is `group add`. `yardr group show <name>` prints one group: its
runner and limit, a setting to a line as `group set` takes it back, the
routes that send it work and the source calls it may make.

## Which beads a route takes

A route is for the open, unblocked beads of its stage. Five filters narrow
it. Four it does not give match any bead; the originator is the one
exception, below:

| flag | the route takes |
|---|---|
| `--type <type>` | beads of this type |
| `--depot <depot>` | beads in this depot |
| `--label <label>` | beads with this label; given more than once, beads that have every one |
| `--bead-priority <n>` | beads of priority n or more urgent: `--bead-priority 1` is P0 and P1 and no others |
| `--originator <user>` | beads for this user alone; `any` takes every originator's, and a route without the flag only the operator's |

```sh
yardr route add --stage new --depot web --label difficulty:3 --label size:large --group heavy-builders
yardr route add --stage new --depot web --bead-priority 1 --group builders --priority 5
yardr route add --stage new --depot web --originator mckean2 --group mckean2-builders
```

A route without `--originator` takes only the operator's beads, however
high its priority or specificity: no general route swallows another
person's work. A bead whose originator has no route waits as an unrouted
bead does, with the one `unrouted` event naming whose beads have no route.
Cross-originator use is opted into per route, visibly in the table:
`--originator mckean2 --group builders` spends the operator's subscription
for mckean2 on purpose (a subsidy route), and `--originator any` spends it
for everyone. A subsidy route is how a new user with no login of their
own is carried on the operator's accounts until they have one. A route
whose first group starts no agents (a script,
people, another yard) takes every originator's beads: `route add` defaults
it to `any`, as packs and the migration do, since none burns a local
subscription.

Whose login a group carries is declared on the group: `login_for=<user>`
in its config, for herdr groups only, naming a user that must exist. A
group with `login_for=mckean2` runs its panes on mckean2's personal login,
carried by its `env_file`; a group without it is the operator's for
routing. Naming the operator means the same as none. `yardr group show`
prints it as any setting, and `yardr doctor` warns when a `login_for`
group names no `env_file`. A route for some originator's beads is personal
or subsidy by its first group: personal when that group declares
`login_for` for the same user, else a subsidy route. `route add` warns on
stderr when a route taking operator beads names a personal-login group
first, since it burns that person's login for the operator's work.

`login_for` is a declaration and the folder is what burns: nothing
enforces that the group's env file points at the declared user's login, so
`yardr group show` prints, beside `login_for`, the account the folder's
login names where the harness writes one (Claude's folder does), and a
mismatch shows to the eye. A group without `login_for` whose folder's
login names an account that is not the operator's is named by `yardr
doctor`: its sessions burn that account while routing attributes them to
the operator. The login itself is made with `yardr harness login <kind>
--group <group>`
([doctor.md](doctor.md#a-harnesss-login-led-by-a-program)), which leads
the login with the group's env so it lands in the folder the env names. A
login made for a group sits in a folder on this machine, which whoever
runs the machine can read: bringing a subscription trusts the operator the
way the machine's own login does.

Of the routes that match a bead, the one with the highest `--priority`
takes it; among routes of one priority, the one that asks for more, where
the type, the depot, the bead priority, each label and a named originator
count as one (`any` counts as none). So a route for two labels is read
before a route for one of them, and a bead with both goes to the first;
an exact originator route beats an `any` route of the same priority.
Routes that ask for as much are read oldest first. A route for urgent
beads that is to win over a route for a label therefore needs a higher
`--priority`, as in the example. `yardr route list` prints the routes in
that order, the labels in the column `LABEL`, the bead priorities as
`P0-P1` in the column `BEADS` and the originator in `ORIGINATOR`.

The labels are yours: yardr gives none of them a meaning, and matches each
exactly. A view that shows who works a stage of a flow (`yardr flow show`,
the web view's flows) asks for a bead made with no label, no priority
(P2) and no originator, so it names the group such a bead goes to — the
operator's; what is said of a bead (`yardr bead show`, a brief's
outcomes, `yardr review`) is by that bead's labels, priority and
originator, as the dispatcher reads them.

## A route with further groups and quota conditions

A route names the group that takes its beads. It may name further groups
with `--else`, in the order they are tried, and each group may carry a
condition after `@` on the windows of its provider's usage limits:

```sh
yardr route add --stage new --depot web \
  --group 'builders@Weekly<80,5h<90' --else spare-builders
```

A bead of this route goes to `builders` while the window its provider
labels `Weekly` is under 80% used and the one labelled `5h` under 90%;
otherwise to `spare-builders`. The dispatcher goes through the groups in
order and takes the first whose condition holds; a group with no condition
always holds. `yardr route list` prints them as
`builders@Weekly<80,5h<90 else spare-builders`.

- **The condition.** Comparisons separated by commas, each a window's
  label, `<` and a percentage above 0 and at most 100. The label is the
  text the yard's limits source reports for the window
  ([integrations.md](integrations.md#say-what-a-subscription-still-carries);
  `yardr limits` prints them), matched exactly, so a window the condition
  does not name does not gate. `@<80` with no label is every window the
  report has. yardr compares two numbers and knows no provider's windows;
  the threshold is yours, and no pack brings one. Whose windows are meant
  follows from the group's `kind` and its `provider` setting, as for a
  harness that stopped on a limit, and the command runs with the group's
  env, so a group on its own login gates on its own windows
  (`yardr limits --group <group>` reads the same). A comparison may name
  a source for the
  window instead: `machine.load<80` reads the window labelled `load` of
  what the source `machine` says of itself, whatever the group's provider
  is (`machine.<80` is every window it reports), and the two forms mix
  (`machine.load<80,Weekly<90`).
- **The pace instead of a number.** In place of a percentage the right
  side may be the window's target with an optional margin:
  `Weekly<target`, `Weekly<target+10`, `Weekly<target-5`. The target is
  the share of the window that has passed, as the limits source reports
  it (`target_percent`); the comparison holds while the window's used
  share is under the target plus the margin, and the two forms mix
  (`Weekly<target+10,5h<90`). yardr never works the target out itself:
  the source is the one that knows the window. A window whose report
  gives no target cannot be compared with one, and reads as any
  comparison that cannot be made does (below): the condition holds and
  the `claimed` event says the limits were not known.
- **Only groups that start agents, or one exec group gated on a source.**
  A route with `--else` or a condition may name only herdr groups, or gate
  one exec group alone on the windows of sources, as
  `refinery@machine.load<80`: `route add` refuses any other (`group
  decisions is manual: --else and a quota condition are for groups that
  start agents`), and `group add` refuses to give a group such a route
  names another runner. A group that starts no agents burns no provider,
  so a condition on one names a source alone. So whichever group takes a
  bead, one runner works the stage and nobody is a member: `yardr review`,
  the beads that wait for a person and who may move a bead on do not
  change with the quota.
- **When no condition holds, the bead waits.** A route with one
  conditioned group and no `--else` says "this group or nobody", as
  `--else wait` says out loud: `wait` is not a group, takes no condition,
  and nothing is tried after it. The bead gets one `quota_wait` event,
  logged once until it starts: `route`, `passed` (each group with its
  `reason` in words and the earliest `resets_at` among its windows that
  failed), `reason` (the same on one line) and `resets_at`, the earliest
  of them all. A source's windows may never reset, and then no `resets_at`
  is said.
- **A group at its limit is waited for, not passed over.** So is one whose
  runtime is down or that is held back for a model. Only a quota condition
  sends a bead to a further group, and what the yard runs at once stays
  bounded by the groups' limits.
- **A condition that cannot be evaluated holds.** No source answers for
  the group's provider, its command failed, the report has no window of
  that label, gives it no percentage or, for a condition on the target,
  no target, and for a comparison on a source no source of the name that
  answers for itself: the group takes the bead as it would with no
  condition, and the `claimed` event says so in `limits_unknown`. A limits
  command that breaks takes the gate away; it does not stop the yard.
- **The `claimed` event** of a bead the dispatcher started names the
  `route` by its id and, when groups were passed over, `passed`: each with
  its `reason`, such as `Claude's Weekly window is 83% used, the route asks
  under 80`, for a condition on the target `Claude's Weekly window is
  38% used, the route asks under target 17% + 10`, or for a comparison on
  a source `machine's load window is 93% used, the route asks under 80`.
  `was` names the group that last held the bead in this stage, when it is
  another: a bead sent back to the stage went to another group than the one
  that worked it before.
- **`yardr bead show` says it in one line**, under the bead's owner, when
  the route had something to say: a group passed over, limits that were
  not known, or another group before. A bead that waits for a condition
  shows what it waits for and when the first of those windows resets, from
  its `quota_wait` event. `--json` has the same as `routing`.

  ```text
    routed to spare-builders by route qp3f…: builders passed over (Claude's Weekly window is 83% used, the route asks under 80)
    waits since 14:02 for a group of route qp3f… whose condition holds: builders passed over (Claude's Weekly window is 83% used, the route asks under 80); the first of those windows resets Sat 17 Oct 09:00 (in 3d 2h)
  ```
- **Conditions that cannot work are said, and none is refused.**
  Groups that burn one provider on one login read the same windows, so a
  further group with no condition that burns the provider of a conditioned
  group before it takes every bead that group was passed over for, as used
  up as it is: `yardr route add` warns of it on stderr and adds the route.
  Two groups on different logins read each their own, so a personal group
  failing over to the operator's warns of nothing. A condition
  on a group no limits source answers for holds whatever is used, as a
  comparison that names a source with no limits of its own does: `yardr
  doctor` notes each such route under `limits`.
- **The choice is made at the claim**, once: a session keeps its group
  until it ends, whatever the window does meanwhile. A bead sent back to
  the stage is routed again and may go to another group. For a stage a
  bead is not in yet nothing is chosen, so a brief's outcome names the
  groups in order (``an agent in `builders` picks it up, or one in
  `spare-builders` while the quota of `builders` is nearly used``), as do
  `yardr flow show` and the flow columns of the web view; none of them
  asks for the limits.
- **How often the limits command runs for this.** Only for a ready bead
  whose route has a condition, when one of the route's groups could start
  it, and at most once in five minutes for a provider read under one
  group's env, or for a source, a failed run included. A group can so
  start a session or two after its
  window crossed the threshold, which a threshold below 100 leaves room
  for. The condition is a margin, not a forecast: sessions started under
  it can still fill the window, and then the harness stops and
  [the existing path](#a-usage-limit-says-its-window-and-when-it-resets)
  takes over.
- **To overrule it**, give a route of higher priority no condition: a
  label's route sends its beads where the label says whatever the quota
  is. Removing a group removes every route that names it, further groups
  included.

## Routing by estimate and quota

An estimate is what someone says of a bead before it is built: how large
and how hard. It needs no new mechanism: it is two labels, and the routes
above match them. The names are the operator's convention — yardr gives
them no meaning — and the starting one is a prefix each, as a topic is
(`topic:routing`): `difficulty:1`, `difficulty:2`, `difficulty:3`, and
`size:small`, `size:medium`, `size:large`. Importance is the bead's
priority, which exists: 0 is highest, 2 the default, matched with
`--bead-priority`.

Who writes it: whoever moves the bead out of `backlog`, by the rubric in
[the yardmaster's role](../packs/starter/roles/yardmaster.md)
("Estimating a bead"): `difficulty:1` follows a pattern the code already
has, `difficulty:2` is new behaviour inside one mechanism,
`difficulty:3` is work whose shape is costly to get wrong; `size:small`
is one session in one package, `size:medium` one session across several
packages or a document, `size:large` more than one session, which should
first be considered for splitting. The judgement is recorded as what the
bead is instead of where it should go, so that the route and not the
yardmaster decides the group, and the quota can change the answer.

With the filters and conditions above, a depot's table for its build
stage reads like this:

```sh
# asked for by hand: no condition, whatever the quota says
yardr route add --stage new --depot web --label heavy --group heavy-builders --priority 6
# the reserve: P0 and P1 go to the strong group whatever the week says
yardr route add --stage new --depot web --bead-priority 1 --group heavy-builders --priority 5
# a large, hard bead needs more room in the window
yardr route add --stage new --depot web --label difficulty:3 --label size:large \
  --group 'heavy-builders@Weekly<60,5h<50' --priority 4
# the heaviest: the strong group or wait
yardr route add --stage new --depot web --label difficulty:3 --group 'heavy-builders@Weekly<80,5h<90' --priority 3
yardr route add --stage new --depot web --label difficulty:2 --group builders --priority 3
yardr route add --stage new --depot web --label difficulty:1 \
  --group 'cheap-builders@Weekly<80' --else 'spare-builders@Weekly<80' --else builders --priority 3
# no estimate: the strong group, and builders when it is nearly used
yardr route add --stage new --depot web --group 'heavy-builders@Weekly<80,5h<90' --else builders
```

A bead with no estimate goes where an unlabelled bead goes: the last
route, with no filter. Nothing is under-served, and the quota condition
is what protects the week.

What the events say of such a table is above: the `claimed` event names
the route and each group passed over with the reason in words, a bead no
condition holds for waits with one `quota_wait` event until it starts,
and `yardr bead show` prints either as one line. And `yardr route
report` reads the events back: for each group and estimate label, how
many beads it built, how often review sent them back, how many sessions
failed, and the median build; and BY HARNESS, one row per harness kind
with its sessions, hours, median and stages. A wrong estimate goes to a
weak group and comes back from review, as a wrong label does today;
someone changes the label and the bead is routed again, and the report
shows the pattern. Nothing changes a route from those numbers: a person
reads them and edits the routes or the rubric.

## What a group's beads cost: the estimate cells

For each group, each `difficulty:` label and each stage the yard keeps
the last 200 stays that ended under a claim: how long each ran from the
claim to the move out, and from the stage entry to the move out, with a
count of every stay and of the exits that sent the bead back. A bead's
ETA is arithmetic over those cells
([line.md](line.md#how-long-the-line-is-the-eta)): its queue wait plus
the medians of its stages ahead, stretched by their send-back share and
shown as a usual range. Nothing scans the log to show one.

A claim stamps the stay it works, with when and by which group; the move
out appends both durations to that group's cell for the stage left, in
the same write as the move. A move that stays in its stage records
nothing. A stay whose claim is gone when it ends — the session failed,
or the bead was released or held — still counts in the stay counts but
adds no duration; a stay no group ever claimed records in no cell. A
failed claim never pairs as a build: a session that failed, died, or got
stuck (blocked on a prompt, stalled on its first, gave up on it, or
stopped on a harness error — the sessions `yardr route report` counts
under FAILED) leaves the claim live but out of the durations, and the
reclaim's durations are the build's.

A move is a send-back when it goes into a stage the bead had already
reached from one it reached later; it is counted at the stage left, in
the cell of the group that worked that stay. Each stage's ETA
contribution is stretched by one plus its cell's send-back share.

The ETA needs 10 claim-to-exit durations in a stage's primary group's
cell before it can time that stage; below that it is unclocked, and the
ETA names it (`review: too new to time`) instead of borrowing another
cell's number. The waiting late clock uses the separate entry-to-exit
ring, with the same 10-sample floor; the running late clock uses the
claim-to-exit ring for the group holding the bead. A waiting late mark
can therefore be available before the claim-to-exit ring can time the
ETA; a quota fallback can also give a running late mark a different
cell from the ETA's primary group. Early on most stages are unclocked,
and the ETAs fill in as stages end.

A yard from before the cells seeds them by replaying its retained log
once, pairing claims with exits the way the route report does. Open
stays start their clocks at the seed: a bead waiting then counts its
stage age from the upgrade, while a running bead's clock is its live
session's start. The waiting late marks take up to one p90 to re-arm.

Late is past the cell's slow time for the stage: a bead no group holds
past the entry-to-exit p90 of its primary group's cell, one a group
holds past the claim-to-exit p90 of the group that holds it. Stalled
means the agent stopped — its first prompt unanswered past the stall
limit, told once as `session_stalled` ([below](#models)); late means it
takes long. A bead can be either, both, or neither. Late is computed
wherever the bead is shown and tells no one: there is no event for it.

## Models

A herdr group's `model` goes to its agent as `--model`; a bead's
`--meta model=…` replaces it for that bead, in every herdr group that works
it. Nothing but the agent's provider knows whether a model may be used: a
name the agent lists may be refused for the account, and one it does not
list may run. An agent given a model it may not use still starts; the
refusal comes with its first request, in its pane, and it then sits idle.
An agent may also run another model than the one asked for without saying
so. The running session is the only place the truth shows, so yardr checks
there, not when the model is set: no command asks an agent about a model.

**The model the agent reports.** Once a bead session's agent has answered
its first prompt, the daemon reads the model the agent itself reports and
keeps it on the session's handle as `model_reported`, next to `model_asked`
(the bead's `metadata.model`, else the group's `model`; absent when neither
is set): `yardr session list --json` shows both. The source is the agent's
own session file, found by the session id herdr reports for the pane:

| kind | source |
|---|---|
| `claude` | `~/.claude/projects/<dir>/<session id>.jsonl` (or under `CLAUDE_CONFIG_DIR`): the `model` of the first assistant message, which is what the API answered with |
| `codex` | `~/.codex/sessions/<y>/<m>/<d>/rollout-<time>-<session id>.jsonl` (or under `CODEX_HOME`): the `model` of the first `turn_context` |
| `muse` | `~/.local/share/muse/sessions/<y>/<m>/<d>/<session id>/session.jsonl`: the `model_id` of the first `run.model.configured` line, which is what the run was started or reconfigured with, not the startup metadata. herdr 0.9.0 reports no session id for Muse panes, so nothing is recorded for Muse until herdr learns its sessions |
| any other | none known: nothing is recorded and nothing compared |

The daemon must run as the user whose agents these are, on their machine,
as it does for herdr anyway. An agent is asked on each pass until it has
answered, for the session's first 15 minutes.

**`session_model_mismatch`.** When the reported model is not the one asked
for, one event per session says so at once, with `asked`, `reported`,
`group` and `stage`. Names are compared by their words and numbers, case and
punctuation aside, and match when all of one name's are among the other's:
`claude-opus-5-5` matches `Opus 5.5` and `claude-opus-5-5-20260801`, an
alias such as `opus` (or `opus[1m]`) matches any Opus, and `claude-opus-5-5`
does not match `claude-fable-5-1` or `claude-opus-5-1`. The session is left
running: the yardmaster decides whether the work of the other model stands.

**`session_stalled`.** An agent that was given its
first prompt, was idle every time the daemon looked since, and has left
nothing behind (no note or move on its bead, no commit and no change in its
worktree) for the group's `stall` (default `2m`) is reported at once, not at
the `idle` limit: one `session_stalled` event per session, on the bead,
with `model`, `group`, `stage` and `tail`, the last 20 lines of the agent's
pane that say anything, where its error is. It is in a new crew member's
default `--on`, as `session_model_mismatch` is, so both reach the yardmaster's
inbox ([subscriptions.md](subscriptions.md#--on)). An agent seen working or
blocked since its prompt is not judged, however long it idles later, and a
prompt held back by a startup question counts from its delivery. Only the
daemon that sent the prompt judges a session; one that replaced itself
since leaves it to `idle`.

The stalled session is kept, as an idle one is. While it sits idle the
group starts no further session with the same model (a bead with another
`--meta model` still starts), since each would stall alike and hold a slot.
The group goes on when the model is changed (`yardr group add` again) or
when someone says to try again by ending the stalled session: `yardr bead
release <bead>`, which also sends that bead to the group again. An agent
that someone got going in its pane lifts the hold as well. `--set
stall=off` turns the check off for a group. Only bead sessions are judged,
not a crew member's.

### A harness that stopped on an error

`session_stalled` is about a session's first prompt. A harness can stop
later as well, on an error of its own, after the agent has worked: the
model is at capacity, a usage limit is hit, the login ran out. It prints
the error and waits for input, which herdr reports as `idle`: to the yard
that is an agent waiting on a human, and without more the yardmaster would hear
of it at the `idle` limit, as of any idle agent.

herdr has the status of some harnesses from their hooks alone, and a turn
that ends on an error may tell it nothing: Kimi, stopped on a usage limit
mid-work, stays `working`. An agent herdr calls `working` on the hooks' word,
whose pane ends in a known error line of its `kind` and has not changed for
a minute, is idle to the yard, and everything below holds for it.

**`session_harness_error`.** The pane says what herdr's status cannot. On
each pass in the first 2 minutes of an idle spell (status `idle` or `done`)
the daemon reads the end of the agent's
pane, and a line among its last 20 that is a known error of the group's
`kind` raises one event on the bead, at once and not at the `idle` limit:

| field | |
|---|---|
| `reason` | `at_capacity` (the model or the API has no room: try later, or another model), `not_logged_in` (log in in its pane, or fix the group's `env_file`), `rate_limited` (a usage or rate limit; which window and when it resets: [below](#a-usage-limit-says-its-window-and-when-it-resets)), `other` (an error line that stops the harness and is none of these: a model it may not use, no credit, an API error) |
| `line` | the error line, as the harness printed it |
| `tail` | the last 20 lines of the pane that say anything, as `session_stalled` carries them |
| `tries` | only when the daemon has prompted the agent again itself before it raised this: how often, in the session's life (see below, for `at_capacity` and for `rate_limited`) |
| `limits`, `window`, `resets_at`, `limits_unknown` | only with `rate_limited`: what a source of the yard says of the kind's subscription ([below](#a-usage-limit-says-its-window-and-when-it-resets)) |
| `wakes_at` | only with `rate_limited`, and only when the daemon waits for the window: when it asks again and prompts the agent ([below](#a-window-that-resets-within-six-hours-is-waited-for)) |
| `model` | only with `wakes_at`, on a session's event: the model the session waits with (the bead's `--meta model`, else the group's; empty for the agent's own), with which its group starts no further session until then ([below](#a-window-that-resets-within-six-hours-is-waited-for)) |
| `prompt_shown`, `prompted` | only on the event for a prompt the daemon gave at a reset and the harness did not take: `false`, and the prompt's text ([below](#a-window-that-resets-within-six-hours-is-waited-for)). It has no `line` |
| `kind`, `session`, `group`, `stage`, `hint` | which harness, whose session, and what can be done |

It is raised once per idle spell: when the agent is seen working again the
spell is over, and an error it stops on after that is told again. The mark
is on the session's handle (`harness_error`, in `yardr session list
--json`), so a daemon that replaced itself does not tell twice. The pane is
read once more when `session_idle` is raised, so an error drawn late is
told then. The session is left as it is, and unlike `session_stalled` the
group goes on starting sessions: whether to wait, to get the agent going in
its pane or to end the session (`yardr bead release
<bead>`) is the yardmaster's to decide. It is in a new crew member's default
`--on`, the class `attention`; a crew member with a list of kinds from
before it was gets it with `yardr crew set <name> --on attention`
([crew.md](crew.md)).

**`at_capacity` on a first prompt is tried again first.** A harness that
answers its first prompt with "at capacity" needs the same prompt again,
later, and nothing else. So for a session whose reason is `at_capacity` and
whose agent has done nothing yet (no note or move on its bead, no change in
its worktree: what `session_stalled` asks), the daemon raises nothing at
first. It waits 5 minutes; if the error still ends the idle agent's pane it
gives the agent its first prompt again (`Read <brief> and follow it.`) and
waits 5 minutes more, up to 3 times in the session's life. Each try is a
`session_harness_retry` event on the bead (`try`, `of`, and the `reason`,
`line`, `kind`, `session`, `group`, `stage` of the error); nobody is told of
it by default. An agent that gets going ends it: nothing is raised. When
the error still stands 5 minutes after the third try,
`session_harness_error` is raised as above, with `tries: 3`, 20 minutes
after the error was first seen. No `session_stalled` is raised while a try
waits; it can follow the `session_harness_error`, as it did before. The
count and the wait are on the session's handle (`harness_retries`,
`harness_retry_at`), so a daemon that replaced itself goes on where the
last one was. Both numbers are fixed; no key sets them.

A session that had done something when it hit `at_capacity` stopped
mid-work, where its first prompt is the wrong thing to say, and one with
text in its input has someone typing there: both are raised at once, with
no `tries`. So are `not_logged_in` and `other`, which asking again does not
mend. `rate_limited` is raised at once too, and an agent whose window
resets within six hours is prompted then
([below](#a-window-that-resets-within-six-hours-is-waited-for)). The 3
prompts of a session's life are counted over both.

The lines known, each a regular expression matched against one line of the
pane with the marks the harness's TUI draws before a message (`⏺ ⎿ ■ •`, the
file's `marks.error`) taken off. A kind's lines are tried in this order, and
the first that matches gives the reason, so a line that says two things (an
API error that ends in `Please run /login`) is under the one that matters
before the general one:

<!-- harness errors: made from harnesses/<kind>.yaml (`errors`), not by hand; YARDR_UPDATE_GOLDEN=1 go test ./internal/agents -run TestGroupsDocTables writes it again -->
| kind | reason | a line that matches |
|---|---|---|
| `claude` | `not_logged_in` | `^(Not logged in\|You are not logged in\|Invalid API key\|Please run /login)` |
| `claude` | `not_logged_in` | `^(Your session has expired\|Session expired)\. Please run /login` |
| `claude` | `not_logged_in` | `^API Error.*(Please run /login\|authentication_error)` |
| `claude` | `rate_limited` | `^You.ve hit your ` |
| `claude` | `rate_limited` | `^(Claude usage\|Usage\|5-hour\|Weekly\|Session\|Fable\|Opus\|Sonnet\|Fast) limit reached` |
| `claude` | `rate_limited` | `^API Error.*(\b429\b\|rate_limit_error\|Rate limit)` |
| `claude` | `at_capacity` | `^Repeated 529 Overloaded errors` |
| `claude` | `at_capacity` | `^API Error.*(\b529\b\|overloaded_error\|Overloaded)` |
| `claude` | `other` | `^There's an issue with the selected model` |
| `claude` | `other` | `^Credit balance (is )?too low` |
| `claude` | `other` | `^API Error` |
| `codex` | `at_capacity` | `^(Selected model is at capacity\|Flex capacity unavailable)` |
| `codex` | `rate_limited` | `^You.ve hit your usage limit` |
| `codex` | `rate_limited` | `^exceeded retry limit, last status: 429` |
| `codex` | `not_logged_in` | `^(Not logged in\|Sign in with ChatGPT)` |
| `codex` | `not_logged_in` | `^Your (access token\|authentication session) could not be refreshed` |
| `codex` | `not_logged_in` | `Please (log out and )?sign in again\.` |
| `codex` | `not_logged_in` | `^(exceeded retry limit, last status: 401\|unexpected status 401)` |
| `codex` | `other` | `^Quota exceeded` |
| `codex` | `other` | `^(stream error: )?stream disconnected before completion` |
| `codex` | `other` | `^(exceeded retry limit\|unexpected status \d)` |
| `kimi` | `rate_limited` | `^Error: \[provider\.auth_error\] 403 You.ve reached` |
| `kimi` | `rate_limited` | `^Error: \[provider\.rate_limit\]` |
| `kimi` | `at_capacity` | `^Error: \[provider\.overloaded\]` |
| `kimi` | `not_logged_in` | `^Error: \[(auth\.(login_required\|token_missing\|token_unauthorized)\|provider\.auth_error)\]` |
| any other | | none known: nothing is reported, and `session_idle` with its `tail` is what there is |
<!-- /harness errors -->

Nearly every pattern begins with `^`: a line counts when it begins with the
words, so an agent that writes about an error in a sentence is not taken
for a harness that stopped on it. An agent whose last message begins with
such a line, quoted, is: the event is a reading of the pane, and `tail` is
there to check it. The lines are `errors` in the kind's file,
`harnesses/<kind>.yaml` ([what yardr knows of a kind](#what-yardr-knows-of-a-kind));
`internal/dispatch/harness.go` says how to add one. A harness's error that
is not there yet shows as a `session_idle` whose `tail` ends in it.

Kimi prints `Error: [<code>] <message>` and breaks the line at the pane's
width itself, so only the line's first part can be matched and `line` is
that part; the rest is in `tail`. The usage limit is a line read from a pane
(6 Oct 2026, Kimi Code 2.1.1). The others are known by their code alone,
from Kimi's list of error codes, and no pane with one has been seen.

**A crew member's harness.** A crew member has no idle limit, so its pane
is read for the same lines when it is idle with something unread in its
inbox: on each pass in the first 2 minutes of that, and for 2 minutes after
each nudge typed into its pane. A known error of its `kind` raises
`session_harness_error` on its journal, with `crew` (its name) where a
session's event has `session`, `group` and `stage`, and with `kind`,
`reason`, `line`, `tail` and `hint`. Whoever hears the kind through `--on`
hears this one too. A crew member that is idle with nothing unread is not
looked at. The reason is kept on its handle (`harness_error`, in `yardr crew
show --json`) and not told again while it stands, through the moments a
nudge makes a stopped harness work: it is told again once a look has found
the pane without an error, or the inbox was emptied. A crew member has no
first prompt to be given again, so nothing is tried for `at_capacity`; at a
usage limit it is prompted when the window has reset, as a session is
([below](#a-window-that-resets-within-six-hours-is-waited-for)). Its nudges
are typed as before, except while such a prompt has not shown in its pane.

**`session_idle` says what the pane shows.** It carries the same `tail`, so
the yardmaster reads why an agent sits there (it asks its human something, it
waits for an answer, its harness says something no pattern knows) without
`herdr agent read`.

#### A command runs in the background

**An agent that waits for its own background work is not idle.** An agent
that starts a long command in the background (the depot's test) and waits
for it is idle to herdr, and its harness wakes it when the command exits. So
when a session reaches `idle`, the pane read for `session_idle` is first
looked at for a line of the kind's `background`
([what yardr knows of a kind](#what-yardr-knows-of-a-kind)), in its last 6
lines that are not empty: what the harness draws around its input, not what
the agent wrote further up. Claude Code's are the line that ends its turn
and its status line (seen 13 Oct 2026, 2.1.288):

```text
✻ Brewed for 48s · done 12:44 AM · 1 shell still running
  ⏵⏵ auto mode on · 1 shell · ← 1 agent · ↓ to manage
```

With one there, no `session_idle` is raised and the idle spell starts again
from that look: the pane is read again one `idle` later, and a `grace` does
not end the session meanwhile. Once the line is gone and the agent still
sits there, it is told of one `idle` after the last look that saw it. What
it does not catch: a background command that never exits, or one the agent
left running while it asks its human something, keeps `session_idle` away
for as long as it shows. `max_age` applies as ever, and an error of the
harness is still told of at the start of the spell. No other kind's file
names such a line, so their agents are told of at the limit as before.

#### A usage limit says its window and when it resets

A harness that stops on a usage limit seldom says, in a line the yard can
read, which window it was or when it opens. A source of the yard with a
`limits` key knows
([integrations.md](integrations.md#say-what-a-subscription-still-carries)).
So when `session_harness_error` is raised with the reason `rate_limited`,
for a bead's session or for a crew member, the daemon asks that source for
the subscription the agent burns — the group's or the crew member's
`provider` setting when set, else the harness's `kind` through the kinds
table — and the event carries the answer:

| field | |
|---|---|
| `limits` | the report: `source`, `provider`, `asked_at`, `plan` when given, and `windows`, each with its `label` and what the provider said of `used_percent`, `target_percent`, `resets_at`, `used` and `limit` (as `yardr limits --json` prints it) |
| `window` | the label of the window the agent waits for: of those used up (`used_percent` at 100, or `used` at `limit`) the one that resets last. Absent when none is used up |
| `resets_at` | when that window resets, RFC 3339 in UTC. Absent when the provider gave no time |
| `limits_unknown` | in place of the three: why nothing is known, in a line. No source of the yard answers for the kind (or names the `provider` setting, where one is set), or its command gave no answer (its exit status or the timeout with the end of its stderr, or what of its output is no report) |

The `hint` begins with it in words, the time as the daemon's clock reads it,
and names the harness's kind even where the daemon asked for a `provider`
setting:

    kimi's 5h window is used up and resets at 00:02 (in 42m); the agent's harness stopped on an error and sits idle; …

A reset on another day has the day (`resets at Sat 10 Oct 09:02 (in 3d
9h)`); one the provider still gives although it has passed reads `was to
reset at 00:02 (3m ago)`. With no window used up the hint says so (`no
window of kimi's subscription is used up, says aiquokka: …`): the limit the
harness hit is then one the provider does not report, another account's, one
model's or a short rate limit, and the yardmaster reads the pane as before.
With `limits_unknown` the hint is what it was.

A session's answer is for the login its group burns, read with the group's
env (`yardr limits --group <group>` reads the same); a crew member's is for
the login the provider reads on this machine, the operator's, whatever its
own env gives its harness.

**When the source is asked.** A provider is slow and is not to be leaned
on, so the daemon runs the source's command in two places. One is while it
raises a `session_harness_error` of reason `rate_limited`: once per idle
spell of a session and once per standing error of a crew member, as the
event is. The other is the wake that event told of (`wakes_at`), once, when
the agent is about to be prompted
([below](#a-window-that-resets-within-six-hours-is-waited-for)): not when
its pane no longer ends in the limit or its input holds text, and not on
the passes of the wait. A pass that finds nothing stopped on a usage limit
asks nothing, and neither does a start, a timer or any other reason. What the
provider said, or that it failed, is kept for 60 seconds in the daemon's
memory, by the provider's name in the source's file and the login read, so
five sessions of one kind on one login that stop in the same minute are one
run of the command; nothing
of it is stored, and a daemon that replaced itself knows nothing. The
command runs inside the dispatcher's pass, so one that hangs holds that
pass for up to 30 seconds, once in a minute. The 60 seconds are fixed; no
key sets them.

The group goes on starting sessions whatever the event says.

#### A window that resets within six hours is waited for

An agent at a usage limit can do nothing until the window opens, and then
needs only to be told. On 6 Oct 2026 a crew member stopped at 23:20 with a
review in its inbox; its window opened at 00:02 and it sat until a person
prompted it. So when the `resets_at` of the event above is no more than 6
hours away, the daemon comes back for it:

| | |
|---|---|
| at the stop | the event is raised at once, as above, with `wakes_at`: the reset and one minute. The hint says `yardr asks again at 00:03 and prompts the agent when the window is open`. The time is kept on the handle (`harness_reset_at`, with `harness_reset_window` and, for a session, `harness_reset_model`), so a daemon that replaced itself wakes for it too |
| while it waits | a session's idle spell is not counted: no `session_idle`, and a group with `grace` does not end it. `max_age` applies as ever. Nothing is typed, and the source is not asked. An agent that is seen working, or a crew member whose inbox was emptied, ends the wait |
| the group, while a session waits | starts no further session with the model that session waits with: each would stop on the same limit, and three would wait where one does. The event says so, with `model`. A bead with another `--meta model` still starts, as beside a stalled session ([above](#models)), and so do the group's beads once its model is changed. The hold is read from the waiting sessions' handles on each pass and asks the source nothing; it ends with the wait: at the prompt, when the agent is seen working, or when the session ends (`yardr bead release <bead>`). `yardr session list` says it under its table: `builders starts no further session with model k3 until 00:03 (in 42m): session … (bead …) waits for the 5h window of its usage limit to reset`, and as `reset_wait` (`until`, `window`, `model`) in `--json`. Another group on the same subscription is not held: its next session stops, is told of, and then holds its own group |
| at `wakes_at` | the source is asked once more. A window that is still used up, with a later reset or none said, is told again as at the stop (a second `session_harness_error`, with the new `resets_at` and `wakes_at`, or none for a reset more than 6 hours off): what the first event said no longer holds. A source that gives no answer does not hold the prompt back |
| the prompt | given when the usage limit still ends the pane, the input is empty and no first prompt is on its way. A session that has done nothing (as `session_stalled` asks) gets its first prompt again. One stopped mid-work gets one line: `yardr: the usage limit your harness stopped on (kimi, 5h window) has reset. Go on where you stopped.` A crew member gets that line and its inbox's count (`… 3 new in your inbox: run yardr inbox to read them, and yardr inbox --ack when handled.`), which counts as its nudge for them |
| the event | `session_harness_retry` on the bead, or on the crew member's journal: `try`, `of`, `prompted` (the text), `window`, and the `reason`, `line` and `kind` of the error. In nobody's default `--on` |
| how often | 3 prompts in a session's life, counted together with the tries for `at_capacity`. An agent that has had them is told of as above, with `tries` and no `wakes_at` |

Not waited for, and left to the yardmaster as before: a reset more than 6
hours away (a weekly window), a window whose reset the source does not say,
a limit of which no source knows, an agent with text in its input at the
wake, and a runner that cannot type into a pane.

**Two minutes after the prompt the daemon looks once at what it did.** A
prompt typed is not a prompt taken:

- The agent was seen working since: it took it. Nothing is said.
- The agent is idle and the prompt does not show in its pane as a message
  the harness took: a `session_harness_error` with `prompt_shown: false`,
  `prompted`, `tries` and the pane's `tail`, and no `line`. The source is
  not asked for it. The harness may be asking something first: Kimi answers
  a prompt that follows a long idle spell with a menu (`This session has
  been idle for 44m and is ~198k tokens. Cache expired — the next message
  re-sends the entire history at full price.` with `Compact and continue`,
  `Start a new session`, `Continue as-is`, `Don't ask me again`), drawn in
  place of its input, and keeps the prompt until it is answered. The daemon
  answers no such question and types nothing more: its first choice costs a
  compaction, and that is for whoever runs the agent to choose
  ([Kimi](#kimi) says how to turn the menu off). A crew
  member's nudges wait until the prompt shows or it works, since a nudge's
  Enter would choose the menu's first answer.
- The prompt shows and a harness error ends the pane below it: the harness
  answered with it, and it is raised as any error is, with `tries`. A usage
  limit asks the source again and may be waited for again.
- The prompt shows and the only error on the screen is above it: that is
  the one it was prompted for. Nothing is said.

The 6 hours, the minute after the reset and the 2 minutes are fixed; no
key sets them.

## Runners

| runner | starts | config |
|---|---|---|
| `manual` | nothing; the bead waits on the group | `notify`, `wait` ([below](#a-bead-that-waits-long)) |
| `exec` | `sh -c cmd` in the depot, with `YARDR_BEAD`, `YARDR_SESSION`, `YARDR_STAGE`, `YARDR_GROUP`, `YARDR_ACTOR` (the group); for a bead with a depot also `YARDR_DEPOT`, `YARDR_DEPOT_PATH`, `YARDR_BASE` (what the bead lands on: the depot's base, or its train's branch, see [line.md](line.md#trains); for a bead from a linked yard that named where its work starts, that commit, see [peers.md](peers.md#code-that-comes-back)), `YARDR_BRANCH` (`yard/<bead>`), `YARDR_WORKTREE` (where herdr groups check that branch out); `YARDR_TEST` (the depot's test command, unset when it has none, see [configuration.md](configuration.md#registering-a-depot)); `YARDR_TYPE` (the bead's type) and `YARDR_TRAIN` (the train it is a child of, empty for none), by which merge-local knows a wagon of a train ([line.md](line.md#wagons)); and `YARDR_BIN`, `YARDR_HOME` for the script's own `yardr` commands; `YARDR_DEPOT_KIND` is `git`, or `dir` for a dir depot, whose beads get no base, branch or worktree. An assembly is one ([line.md](line.md#4-approved-the-assembly-lands-it-on-main)), and so is an integrator such as `yardr-integrators`, which integrates a train's branch once its children have landed ([line.md](line.md#the-trains-own-flow)) | `cmd`, `dir`, `log`, `worktrees`, `env_file` |
| `herdr` | a herdr worktree workspace on branch `yard/<bead>` (from the bead's base) at `~/.yardr/worktrees/<depot>/<bead>` (reopened for later stages, one tab per stage), an agent in it, in a tab of its own with `YARDR_ACTOR` (the agent), `YARDR_BEAD`, `YARDR_SESSION`, `YARDR_STAGE`, `YARDR_GROUP`, `YARDR_BASE` (what it branches from: the depot's base, or its train's branch), `YARDR_HOME` and `YARDR_BIN` in its environment, prompted with a brief in the worktree's git dir (`git rev-parse --git-path yardr`); a dir depot's bead works in the directory itself (see [Dir depots](#dir-depots)) | `kind`, `prompt`, `args`, `model`, `keep`, `worktrees`, `idle`, `max_age`, `grace`, `stall`, `label`, `env_file`, `trust` |
| `peer` | nothing here: the bead is sent to a linked yard, which works it in a flow of its own and returns how it ended ([Peer groups](#peer-groups)) | `peer`, `tries`, `remote`, `max_age` |

What an exec session's command prints, stdout and stderr, is appended to
the group's `log`, one file for all its sessions. A group with no `log`
gets a file per session, `<home>/logs/sessions/yard-<session>.log`, in the
yard's home so that it goes when the home does. The session's handle names
the file either way (`yardr --json session list --all`, `handle.log`).
Nothing prunes these files.

An exec group takes `env_file` as a herdr group does
([Outbound credentials](#outbound-credentials)): what its script needs to
act outside the yard, such as the path of a deploy key, comes from a `0600`
file, not from `cmd`, which is in the store, nor from the environment the
daemon happened to be started with.

```sh
yardr group set signalbox-deploy env_file=~/.yardr/deploy/signalbox.env
```

Each entry joins the command's environment over one of the same name the
daemon was started with. A key yardr sets itself is ignored, and so is one
the command gets from the daemon neither: a setting of the yard's server,
`YARDR_TEST` for a depot with no test command, and for a dir depot's bead
`YARDR_BASE`, `YARDR_BRANCH` and `YARDR_WORKTREE`. The file is read at each
start, so a changed one holds from the next session; only its key names are
logged (`env_file loaded`, at debug). A file that is missing, not a regular
file or open to others stops the group as it stops a herdr group: the
command is not run, the session is aborted without counting against the
bead, and the group backs off and logs `runtime_down` once with the reason
until the file is fixed.

### How many sessions the machine carries

A group's `limit` is how many sessions that group has at once. Nothing
adds the groups up, so a yard with three groups of limit 3 may run nine
agents and a landing's tests beside them. The setting `max_sessions` is one
number for all of them, the operator's statement of what the machine
carries:

```sh
yardr settings set max_sessions 4
yardr settings set max_sessions ""    # no cap, as is 0
```

The daemon checks it beside the group's limit before it starts a session:

- A session of a `herdr` group counts, and so does one of an `exec` group:
  a landing that runs the whole suite is the heaviest thing on the machine,
  and it takes a place like an agent. An exec group has no number of its
  own; its `limit` is that.
- A `manual` group's claim does not count (a person's turn, nothing runs),
  nor a `peer` group's (the other yard does the work).
- The crew does not count: a crew member is there because the operator
  added it, and the daemon keeps it running whatever the cap is.

A bead the cap alone keeps from starting gets one `capped` event with the
setting in its `reason`, as a bead in `backoff` gets one; a bead that waits
for its own group's limit gets none, as before. No group has a share of
the cap: a place that comes free goes to the first bead the daemon would
start anyway, whatever its group, so a landing can wait behind builders.
Lowering the setting under what runs stops nothing; no session starts
until enough have ended.

The setting is read on every pass of the daemon, so raising it starts the
beads that wait on the next pass. `yardr status` shows it on its `work`
line, with how many sessions count against it:
`3 sessions running (web/assembly 1, web/builders 2), 3 of max_sessions 4`.

### A bead that waits long

Nothing runs in a manual group, so no session of it goes idle or ages, and
a bead nobody was told of waits there unseen. How long is too long is the
group's to say, as `idle` is a herdr group's:

```sh
yardr group set backlog wait=1d
```

`wait` is a duration (`40m`, `12h`, `7d`). A bead that has been longer than
that in a stage of the group raises one `waited` event, with the group, the
stage, `since` (when it entered the stage) and `waited` (how long that is).
It is raised on the daemon's pass, by its clock, whether the group holds the
bead or is at its `--limit` and leaves it unclaimed. It counts from when the
bead entered the stage and not from its last note, so a bead that is talked
about and never moved is told of too.

`waited` is of the class `attention`
([subscriptions.md](subscriptions.md#the-class-attention)): the yardmaster
has it in its inbox, and so does a crew member or a user that subscribes to
the class. It is raised once for each stay in the stage. A bead that leaves
and comes back is told of again when it has waited that long anew; one that
stays is not, however long. A group with no `wait` raises nothing, and there
is no default.

The stay is read from the bead's events, where the `waited` raised for it is
also what says it was told. A bead that has waited longer than the yard
keeps events ([configuration.md](configuration.md#retention-keeping-yarddb-bounded), 90 days) is
counted from when it was made and may be told of once more.

### Peer groups

A group whose runner is `peer` has its beads worked by another yard
([peers.md](peers.md#the-sending-side-a-stage-worked-by-a-linked-yard)). It
is the fourth kind of worker next to people, agents and scripts, and like
them it is a matter of the route, not of the flow file:

```sh
yardr peer allow north --send bead
yardr group add at-north --runner peer --set peer=north --limit 3
yardr route add --stage build --group at-north --depot web
```

```text
new -> build
build -> review on done | decide on question | decide on failed | decide on refused | decide on undelivered
review -> merged
decide -> build
merged terminal
```

| setting | what |
|---|---|
| `peer` | the linked yard, by this yard's name for it (`yardr peer list`). Required; refused when there is no such peer or its link does not carry `bead` out. `yardr peer rename` changes it with the link |
| `--limit` | how many beads may be at that yard at once |
| `tries` | how often the bead is posted before its stage ends as `undelivered`; 5, about a quarter of an hour. Also how often a branch that came back is fetched before the bead is held |
| `remote` | the remote of the bead's depot that a branch the yard returns is fetched from, by its name (`git remote`); `origin` ([peers.md](peers.md#code-that-comes-back)) |
| `max_age` | how long a bead is away before `session_aged` is raised, once, naming the command that reads the other yard's bead (`read=yardr thread show <bead>`); 24h. Nothing stops the session at that age or after it: `grace` and the cap at twice `max_age` do not apply to a bead at another yard |

A peer group has no members and no prompt. Its session (`yardr session
list`, runner `peer`) is the bead being away: it ends when the other yard's
return comes, with that yard's outcome reported by `peer:<name>`, or with
one of the yard's own, `refused` and `undelivered`. A stage that does not
name the outcome that came keeps the bead, held, with a note. A return that
names a branch moves the bead once that branch is fetched and checked, into
the bead's own `yard/<bead>`. `yardr bead
show` says `at north: sent 10-06 14:02, north last heard 10-06 14:10`, and
under it, over a link that carries `thread`, how the bead stands there
([peers.md](peers.md#while-a-bead-is-at-the-other-yard)), and
`yardr flow show` has `yard north` under MEMBERS. A yard without a key
(`yardr peer key`) can send nothing: the group is reported as blocked, as a
group whose `env_file` is refused is, and its beads wait.

### Dir depots

A dir depot ([configuration.md](configuration.md#depot-kinds-git-and-dir)) is a plain directory,
with no git, base or branches, so its beads are worked in the directory
itself. A herdr group opens a workspace there for the bead (`herdr
workspace create --cwd <dir>`, labelled as any bead's; later stages reuse
it while it is open), a tab per stage with `YARDR_DEPOT` and `YARDR_DEPOT_PATH`
in place of `YARDR_BASE`, and writes the brief to `<dir>/.yardr/briefs/`.
The brief says to work in the directory directly, commit nothing, touch
only what the bead names and say in the note what was produced, by path.
Once the bead closes its briefs are removed and its workspace closed; the
directory is left as it is, so there is nothing to `keep`. An exec group
runs in the directory with `YARDR_DEPOT_KIND=dir` and no `YARDR_BASE`,
`YARDR_BRANCH` or `YARDR_WORKTREE` (not even ones the daemon inherited).
Trains there only group: no train branch is made and children work in the
same directory. The integrator, assembly and pull request scripts in
`contrib/assembly` work on branches, so a dir depot's flow must not route to
them (routes are per depot): its train flow sends `open` to reviewers instead,
and each script refuses a dir depot's bead with exit 2 as a guard. Several
beads may work the same directory at once; the group's `limit` is the only
control, so set it to 1 where their files could collide.

A session ends when the bead is advanced, closed or released. If the runner
reports the work gone (process exited, herdr agent gone) the session is marked
`died` and the bead is routed again after a backoff; after 3 failures in one
stage the bead is held (`yardr bead unhold` grants fresh attempts).
A runner that cannot start anything (herdr missing or down) is an outage, not
a failure of the bead: the group backs off and logs `runtime_down` once.
Groups can also limit how long a session sits idle or runs (`idle`,
`max_age`, `grace`; [configuration.md](configuration.md#session-limits-and-outages)).
A session that waits for the window of a usage limit to reset is not idle
([above](#a-window-that-resets-within-six-hours-is-waited-for)), and neither
is one whose pane says that a command it started in the background still
runs ([above](#a-harness-that-stopped-on-an-error)).
`session_idle` carries the end of the agent's pane as `tail`, and an agent
whose harness stopped on an error is reported sooner, as
`session_harness_error`
([above](#a-harness-that-stopped-on-an-error)).

### The prompt

A herdr group's `prompt` is the role its agents start from, at the head of
their brief. A group of a depot set up from a pack has the absolute path of
the pack's installed role, `~/.yardr/packs/<pack>/roles/builder.md`, read as
is. `--set prompt=roles/builder.md` reads the depot's own
`.yardr/roles/builder.md` as it is on the depot's base branch (a bead cannot
change its own reviewer's prompt), else `~/.yardr/roles/builder.md` in a
yard that has it from an earlier yardr. The order and the older depot-root `roles/` are in
[configuration.md](configuration.md#role-prompts).

### Role file or stage file

A brief has two texts that are not about the bead: the group's role prompt
and the stage's [stage file](flows.md#stage-files-what-a-stage-is-for). They
answer different questions.

- **The role file** says how this group works, in any stage it is routed
  for: how a builder builds (style, the tests, never `go install`), how a
  reviewer reads a diff, which tools to use and which to leave alone, what
  to do with something outside the bead. It follows the group.
- **The stage file** says what the stage is, for whoever works a bead
  there: what it is for, what must be true before a bead leaves it, and when
  each outcome applies (`changes` when the builder can fix it, `question`
  when a person must decide). It follows the flow, so a person or a crew member
  in a manual stage reads the same text with `yardr prime --bead`, where no
  role prompt reaches.

If a sentence would still be true after another group took over the stage,
it belongs in the stage file; if it would still be true after the group was
routed to another stage, in the role file. Stages no agent works (`backlog`,
`decide`) have no role to say it in, only a stage file.

### Outbound credentials

A herdr group whose agents need a credential to act outside the yard (a
review bot's GitHub token, say), or an exec group whose script does
([above](#runners)), names an env file with `--set
env_file=~/.yardr/groups/<group>.env`. The rules are the crew's
([crew.md](crew.md#outbound-credentials)): `KEY=VALUE` lines, `#`
comments, no expansion, mode `0600` or stricter, and the store never holds
the values (BDP serves it, backups copy it, every builder reads it). Each
entry joins the agent's tab environment next to `YARDR_ACTOR` and the rest;
a key yardr sets itself is ignored.

```sh
yardr group add yardr-reviewers --runner herdr --set kind=codex … \
  --set env_file=~/.yardr/groups/reviewers.env
```

A file that is missing, not a regular file or open to others stops the
group, not the bead: nothing is created, the session is aborted without
counting against the bead, and the group backs off and logs `runtime_down`
once with the reason, as for a runtime outage, until the file is fixed.

### What yardr knows of a kind

herdr starts an agent of a group's `kind` and says whether it is idle,
working, blocked or done, by its own rules
(`~/.local/state/herdr/agent-detection/`). What yardr knows of a kind beyond
that is in one file per kind, `harnesses/<kind>.yaml` in yardr's source,
built into the binary and read once when yardr starts:

| in the file | what it says | read by |
|---|---|---|
| `version` | the shape of the file, 1 today; a file of another shape is refused | the loader, `yardr doctor` |
| `label` | the harness's name as people write it | the forms, `yardr harness show`, `yardr doctor --json` |
| `provider` | who makes the harness, as people write it. Claude Code, Codex, Kimi and Grok name one each; a kind whose README names no maker names none | `yardr harness show`, `yardr doctor --json`, for a program that shows them |
| `aliases` | other names the kind is asked for by; no file has one today | the loader |
| `install` | where to get the harness; printed, never run | `yardr doctor`, the onboarding |
| `fetch` | how the harness gets onto a Mac that has no package manager and nobody at a terminal: one file of one version at its maker's address, with its sha256 ([doctor.md](doctor.md#a-program-fetched-on-a-mac)). Printed, never fetched by yardr. Claude Code and Codex have one; a kind with none is installed by hand | `yardr doctor --json`, `yardr harness show --json`, for a program that fetches |
| `trust` | the folder-trust dialog yardr answers: `dialog` (the text that marks it), `answer` (the one that accepts it) and `cursor` (the mark of its selected answer); a kind with none is left for a person | the herdr runner, at start |
| `marks` | `input`, `message`, `edge`: what its pane is read by ([herdr setup](#herdr-setup)); `error`: the marks taken off a line before it is read for an error | the herdr runner; the daemon |
| `flags` | `model` (selects the model), `unattended` (what `yardr init` gives the yardmaster and the forms preselect), `version` (prints its version and exits), `one_shot` (answers one prompt and exits: `yardr doctor --try`), `always` (added to every agent of the kind: [Codex](#codex)'s `--no-daemon` and its update check off), `permissions` (named flag sets, each with `label`, `args`, `description`). `{socket}` in an argument, a preset's or a group's own `args`, is the path of the yard's socket, put in by the runner when the agent starts: for a kind whose sandbox denies unix sockets and can be told of one to let through (the `read only` presets). `{workdir}` is the directory the session works in (a bead's worktree, a crew member's directory), put in the same way: for a sandbox that is told what to deny by an absolute path (Claude Code's `read only`) | `yardr init`, `group add`, `pack apply`, `doctor`; the herdr runner |
| `offers` | the models to pick from: each a `model` id the kind's `--model` flag takes, a `label` a person reads, and what it is `recommended` for (`yardmaster` on the one frontier model a yardmaster should run on). A suggestion, not a check: `init --model` takes any id, whether the file names it or not. Claude Code, Codex and Kimi name one each; a kind with none suggests no model | `yardr harness show`, `yardr doctor --json`, for a program that offers them |
| `model` | where the model an agent reports is read ([Models](#models)): `home` and `home_env` (the harness's own directory), `glob` (the session file, `{session}` being the id herdr reports), `type` and `field` (which line, and where in it; `type_field` names the key that holds the type when it is not `type`) | the daemon |
| `errors` | the lines it stops on, each a `pattern` and a `reason` ([above](#a-harness-that-stopped-on-an-error)) | the daemon |
| `dialogs` | the questions it draws in place of its input, each a `pattern` for a line of the question and the word for it, `dialog` (yardr cancels Claude Code's auto mode offer; other dialogs are told of and left for a person; [Kimi](#kimi) and [Claude Code](#claude-code) have one each) | the herdr runner |
| `login` | the lines of its login screen, drawn in place of its input when it has no login, which yardr types nothing into and tells of as not logged in: each a `pattern` for a line of the screen (Claude Code and Muse have one) | the herdr runner |
| `login_recipe` | how a program leads its login for a person with no terminal ([doctor.md](doctor.md#a-harnesss-login-led-by-a-program)): the harness's own login command (`argv`), the ways to log in it has flags for (`methods`, each a `label` and its `args`), and patterns for two lines it prints, the one with the page to sign in on (`open_url`, whose one group is the address) and the one it takes input on (`input`), with the command and version they were read from (`verified`). Claude Code has one; a kind with none is logged in in a terminal | `yardr harness login <kind> --json`; `yardr doctor --json`, `yardr harness show --json`, for a program that leads it |
| `background` | the lines it draws around its input while a command its agent started in the background still runs, each a `pattern`: a session whose pane shows one at its `idle` limit is not told of as idle ([above](#a-command-runs-in-the-background); Claude Code has two, no other kind has any) | the daemon |
| `hints` | what the kind adds to a hint of yardr's, a sentence each. `reset`: what the harness may ask before it takes the prompt given at a usage limit's reset, for the event that says the prompt [was not taken](#a-window-that-resets-within-six-hours-is-waited-for) ([Kimi](#kimi) has one; no other kind has any) | the daemon |

```sh
yardr harness list           # the kinds with a file, its version, what each says
yardr harness show codex     # everything the file says, as yardr reads it; --json for a program
yardr harness login claude   # what to run in a terminal to log in; --json leads the login for a program
```

`yardr doctor` names the file's version beside each harness it finds
(`; harness file v1`, or `; no harness file`). A kind herdr starts that has
no file is started all the same: yardr knows no marks, no error lines and
no flags of it, and nothing here applies to it. There is one set of files,
the one in the binary: a yard cannot add to it or replace one. Teaching
`yardr group show` prints the kind's `always` flags beside the group's
settings, and `yardr doctor` prints them beside the harness file version;
`--json` includes them as `harness.always` and `machine[].harness_always`.
Teaching yardr a kind, or a quirk of one, is an edit to its file and a test
with the line or pane it was seen in; the tables of this page that show
`errors` and `marks` are made from the files (`TestGroupsDocTables` in
`internal/agents` fails when they differ). How a kind lists its models is
code (`internal/agents/models.go`), since each asks its own tool.

### Claude Code

Claude Code (seen with 2.1.288) offers to set auto mode up once auto mode
has blocked several actions of a session and `~/.claude/settings.json` has
no `autoMode.environment`. The offer is drawn in place of its input at the
end of a turn, and stays until it is answered:

```text
 Teach auto mode about your environment?
 Auto mode works better when it knows your environment. Takes about a minute.
 1. Yes
 2. Not now
 3. Don't show again
 Enter to confirm · Esc to cancel
```

It was seen in the pane of a builder that waited for a command in a
background shell, and the session was told of as `session_idle` five
minutes later. Esc in the pane made it go (the yardmaster sent it with
`herdr agent send-keys`).

Turn the offer off where the agents run, in `~/.claude/settings.json` of
the user the yard runs as. It is Claude Code's own setting
([Configure auto mode](https://code.claude.com/docs/en/auto-mode-config#turn-off-auto-mode-setup)),
and takes the `/auto-mode-setup` command away with the offer:

```json
{
  "skillOverrides": {
    "auto-mode-setup": "off"
  }
}
```

The other way is to give auto mode what it asks for: with entries under
`autoMode.environment` in the same file the offer is not made. yardr sets
neither for you: the file is yours, and what auto mode may trust is yours
to say. Nor does it pass the setting as an argument: a second `--settings`
beside the one of the `read only` preset has not been tried against Claude
Code, and that one carries the sandbox.

yardr knows the offer by `dialogs` in `harnesses/claude.yaml` (the word is
`auto_mode_setup`, the line the offer's title). When it sees it, the runner
sends Esc once to cancel it, and does not choose an answer: "Yes" starts a
setup that writes to your settings. A held prompt or nudge is then delivered
after Claude Code returns to its input. The runner waits for the dialog to
redraw before sending Esc again, so a delayed repaint cannot send repeated
keys to the agent's input. You can also turn the offer off in Claude Code's
settings as above.

### Codex

Codex (seen with codex-cli 0.160.0) runs its shell commands in a background
server shared by all of the user's Codex sessions (`codex app-server
--managed-daemon`), which outlives the session that started it and keeps
that session's environment. Every later session's `yardr` commands then
carry the first one's `YARDR_ACTOR`, `YARDR_SESSION` and `YARDR_BEAD`:
notes are attributed to another bead's agent, and `yardr bead done` is
refused as stale, leaving the bead with an idle session (the refusal is on
the bead as `move_refused`). Two things hold
against that:

- **A `kind=codex` agent is started with `--no-daemon`**, bead agent and
  crew member alike, whatever the group's `args` say. Its commands then run
  under its own pane, with the tab's environment, and the store recognises
  them by the process tree as it does any agent's
  ([line.md](line.md#2-new-a-builder-implements-it)). The installed
  Codex must know the flag (`codex --help`).
- **The worktree names its session.** A herdr group writes who it started
  in a bead's worktree to `session.json` next to the brief (bead, session,
  actor, stage, group; replaced by each stage). A `yardr` command whose
  `YARDR_BEAD` names another bead than the worktree it runs in acts as the
  worktree's session, not the environment's, and says so on stderr: its
  note is that agent's and its move is checked as that session. So an
  agent whose shell is shared all the same (a Codex started by hand, or
  another kind that does the same) is not taken for another, as long as it
  runs `yardr` from its worktree. Only another bead's environment gives
  way: one naming the worktree's own bead is an earlier stage's agent left
  open and stays refused as stale, and one naming no bead (a person, a
  crew member) acts as itself. A pane the store knows still wins: a call from
  another session's own pane is refused, wherever it stands.

A `kind=codex` agent is also started with
`--config=check_for_update_on_startup=false` (`-c key=value` in one word, so
that it stands beside a group's own `-c`), whatever the group's `args` and
your `~/.codex/config.toml` say: Codex otherwise looks for a release of
itself at start and upgrades in its own pane before it draws its screen
(`brew upgrade --cask codex`, for minutes), and a session must not change
the machine's software. `yardr harness show codex` lists both flags as
`always`. Upgrade Codex yourself, between sessions. A pane that shows
something else than the harness's screen all the same is typed nothing: see
[a harness that has not drawn its screen](#herdr-setup).

A dir depot's beads share one directory, which names no session: there only
`--no-daemon` holds. Sessions started before this (still on the shared
server) keep the wrong environment until they end; spell the values out
for them, `YARDR_ACTOR=… YARDR_SESSION=… YARDR_BEAD=… yardr bead done …`.

### Kimi

Kimi Code (seen with 2.1.1) answers a message that follows a long idle
spell, longer than its model's cache lasts and with a context above a token
threshold, with a menu drawn in place of its input, and keeps the message
until the menu is answered:

```text
 This session has been idle for 44m and is ~198k tokens.
 ↑↓ navigate · Enter select · Esc cancel
 Cache expired — the next message re-sends the entire history at full price.
  ❯ Compact and continue    one-time compact cost · cheapest way to keep this topic
    Start a new session     zero context cost · best for a new task
    Continue as-is          full history kept · highest cost per turn
    Don't ask me again
```

A nudge, a first prompt or the prompt at a usage limit's reset typed after
such a spell meets it. yardr answers no dialog of a harness for you: a
compaction, or 198k tokens sent again at full price, is yours to choose.
What it does while the menu is on:

- It types nothing into the menu. The selected answer is marked as an
  input is, so the pane reads as an input that holds text, and a nudge's
  Enter would choose that answer.
- It tells, at once and once: one `nudge_held` on the crew member's journal
  (on the bead, for a bead's session) with `reason: dialog`, `dialog:
  cache_menu`, `text` (the menu's first line, with how long the session sat
  and how large it is) and the pane. That is on the pass that finds the
  menu, whether a nudge was held back by it or the nudge just typed is the
  message Kimi keeps behind it; later nudges into the same menu raise
  nothing. `nudge_held` is of the class `attention`, so the yardmaster's
  inbox has it.
- Once the menu is answered in the agent's pane, Kimi takes the message it
  kept, and nudges go on as before.

To have no such menu in an agent's pane, turn it off in Kimi: `cacheExpiryHint
= false` in Kimi's `tui.toml`, or choose "Don't ask me again" once in the
pane of each agent. Every message after an idle spell then sends the whole
history without asking.

The menu is known by `dialogs` in `harnesses/kimi.yaml`: a line around the
pane's input mark, up to the rule above it, that begins with `This session
has been idle for` or `Cache expired`. `yardr harness show kimi` lists them.

That Kimi keeps the prompt given at a usage limit's reset behind the menu is
what the `hint` of that prompt's `session_harness_error` says for a Kimi
agent, in the words of `hints.reset` in the same file. For a kind whose file
has no such hint the event names no harness.

### herdr setup

- **Folder trust.** Claude Code, Codex and Kimi ask whether they may trust
  a folder the first time they are started in it, and none has a flag or
  setting that skips the question in an interactive session. yardr answers
  the dialog the kind's file names (`trust` in harnesses/<kind>.yaml): when
  the agent stops at it, yardr moves the cursor onto the accepting answer
  and sends the first prompt as soon as the agent is ready. Making
  a folder a depot (or a crew member's directory) is what says you trust it. The
  session's `started` event carries `trust_accepted: true` when this
  happened. Claude Code and Codex record the answer against the repository,
  so a bead session meets it once per depot's worktree; Kimi records it per
  folder, so it comes up in every new worktree. Claude Code keeps no trust
  for the home directory, so a crew session there meets the dialog at every
  start, and the yard answers it each time. Set `--set trust=false`
  on a group (or crew member) to answer it yourself: the session then waits
  as `session_blocked` and gets its first prompt once you have. yardr
  answers only this dialog, only when both its mark and its answer show and
  its cursor is seen on the answer; anything else an agent asks is left to
  you. Under load the dialog may draw only after the start looked, or over
  the delivery itself: while the first prompt is pending and never taken,
  the daemon's passes answer it the same way, and a delivery herdr reports
  blocked at a dialog counts as no delivery when a readable pane has no
  prompt echo. An echo, a prior send, or a pane that cannot rule one out
  keeps the send guarded. Once a delivery was seen taken — the prompt shows
  in the pane, or the agent was seen working after a send — the answer is
  left to you: the agent's work reaches its pane, and a dialog's text there
  may be a file it shows at a permission question Enter would answer. A crew
  delivery counted but never taken — dropped by the agent, or sent by a start
  that could not look — is answered when the session never worked and the
  live pane has no echo of the prompt; `prompt_started`, a shown or typed
  prompt, or working history refuses.
- **A first prompt that gets lost.** herdr reports a prompt as sent, not as
  taken: an agent that is still coming up can drop the keys. So yardr gives
  the first prompt to herdr to wait on (`herdr agent prompt <agent> '<text>'
  --wait --until working --until blocked --timeout 6000`; herdr 0.9.0 has
  the wait, and yardr asks for it whatever herdr it finds). herdr watches
  the agent for five
  seconds and says what it saw: the agent working, the agent at a dialog
  (`blocked`), or neither (`agent_prompt_stalled`; yardr reads a `timeout`
  and any other error of herdr's as that too). The wait is herdr's and the
  look is yardr's: a stalled prompt may have reached the agent all the
  same, so after herdr's answer yardr reads the agent's pane, once. A
  prompt that does not show is
  kept on the session (`pending_prompt` on its handle) and sent again once
  the agent is idle, unless it has shown up in the pane by then. Only the
  prompt showing ends that look for good: an agent herdr saw working is
  taken to have it, and so is one whose pane could not be read, but one
  reading can be wrong, so that prompt stays on the handle too
  (`prompt_unconfirmed: true`) until the daemon's next pass has looked
  again. An agent herdr saw at a dialog after the prompt keeps it pending
  unless the pane shows it taken, and one that was at a dialog before is
  typed nothing (herdr refuses with `agent_blocked`, since the keys would
  answer the dialog): on later passes the trust dialog is answered as above,
  and anything else waits for you as `session_blocked`, like an agent that
  was not ready. What the pane showed and what herdr said is
  `prompt_look` on the handle (`shown, herdr working`; `typed, herdr
  stalled`: the pane's part is `shown`, `typed`, `no input`, `absent`,
  `unknown`, `unreadable` or `no pane`, herdr's is `working`, `blocked` or `stalled`;
  `no screen, not typed` is a prompt that was not given to herdr at all,
  see below),
  in the `started` event and in a `session_stalled` or
  `session_idle` after it. So is the
  prompt of an agent that was not ready at its start, such as one you had to
  answer a question for: the daemon delivers it once the agent has been
  idle for three seconds (it has just left a dialog and may still be drawing
  its screen), and keeps it until it arrived, which is when it shows in the
  pane or the agent is working. The daemon's delivery goes out with the
  same wait: one that stalls with its text in the agent's input gets its
  Enter on that pass, and one for an agent at a dialog is not typed and
  not counted, as one that ends with the agent at a dialog counts none
  either when the pane has no echo of it (herdr reports `blocked`: the agent
  took nothing). A pane that cannot be read keeps the send guarded. A
  delivery that has done neither after ten
  seconds was lost (`session_prompt_lost`) and is made again, three times in
  all; after the third, `session_prompt_gave_up` tells the yardmaster, its `hint`
  being the command that gives the prompt by hand (`herdr agent prompt
  <agent> '<text>'`), and the session is left as it is. A prompt given by
  hand ends the wait like any other that shows. A prompt whose text is in
  the agent's input and was not submitted has not arrived either: "shows"
  means above the input, where a harness echoes a message it took. The
  pane is read by two marks of its kind (`kind` on the session's handle):

  <!-- harness marks: made from harnesses/<kind>.yaml (`marks`), not by hand; YARDR_UPDATE_GOLDEN=1 go test ./internal/agents -run TestGroupsDocTables writes it again -->
  | kind | the input begins with | a message it took begins with |
  |---|---|---|
  | `claude` | `❯` | `❯` |
  | `codex` | `›` | `›` |
  | `kimi` | `>` after `│`, the edge of the box its input is drawn in; the edges are no part of the text | `✨` |
  | `muse` | `❯` | `❯` |
  | any other, or none | `❯` or `›` | no mark known: the prompt anywhere above the input, and anywhere at all in a pane with neither mark, as before |
  <!-- /harness marks -->

  The input is what the pane holds from its last line that begins with the
  input mark. A harness that is still starting has drawn no input yet, and
  the pane's terminal echoes keys that come then. So in the pane of a
  `claude`, `codex` or `kimi` session only that kind's marks tell: without
  its input mark the text proves nothing, and the look ends as `no input`,
  the prompt pending; and above the input
  the prompt shows only where it begins a line with the message mark, as a
  message the agent took does, never as the echo the terminal left there.
  The marks are `marks` in the kind's file, `harnesses/<kind>.yaml`.
  With the agent idle the daemon sends Enter alone, never the text a second
  time (`session_prompt_typed`, its `submit` the count), three times in
  all, ten seconds apart; after the third, `session_prompt_gave_up` has
  `typed: true` and the hint `herdr pane send-keys <pane> enter`.
  `session_prompted` is the arrival, not the sending: `shown: true` when the
  prompt was found in the pane, `deliveries` how often the daemon had sent
  it, `submits` how often it had sent Enter for it, and `unseen` (a
  sentence) when the pane showed the prompt neither taken nor in the input
  and herdr had seen the agent start to work on it: the agent answered with
  more than a screenful, the prompt left the screen, and it is not sent a
  second time (see "How a pane is read" below). Crew members go the same
  way, with the events on their journal. Text in the input that is not the
  prompt is someone's: the daemon does not type the prompt after it, and
  delivers it once the input is empty (see "Text in the input" in
  [crew.md](crew.md); `nudge_held` is then on the bead).
  Whatever `prompt_look` said, yardr submits its own text left in an idle
  agent's input. On every pass the daemon reads the input of each idle
  session and crew member that has no prompt pending. If it holds what
  yardr typed there and nothing else (the first prompt, the prompt given at
  a usage limit's reset, a crew member's last nudge; also the same text
  twice over, from a second delivery into a harness that had no input yet),
  and the agent has been idle on it for three seconds, the daemon sends
  Enter: `session_prompt_typed`, whose `text` names which and whose
  `reason` says that nothing was pending, with `times` when it was there
  more than once. Enter goes three times in all, three seconds apart; then
  `session_prompt_gave_up` has `typed: true` and the same hint. This
  catches a look that was wrong (`shown` on a prompt that was only
  typed) and a nudge whose Enter was lost. A nudge herdr saw stall does not
  wait for that pass: it gets its Enter on the one that typed it (see
  "Text in the input" in [crew.md](crew.md)). Only a `claude`, `codex` or
  `kimi` pane is read for it: in any other, and in the pane of a session whose handle
  has no `kind`, yardr cannot tell the input and sends no key. A crew
  member's first prompt is known from `first_prompt` on its handle, so one
  started before yardr wrote that is covered from its next start.
- **A harness that has not drawn its screen.** herdr can report an agent
  idle while its pane still shows something else: a Codex that upgrades
  itself before it starts (`brew upgrade --cask codex`), a slow start. Keys
  typed then go to whatever runs there. So for a `claude`, `codex` or `kimi`
  session, a pane that shows no line beginning with the kind's input mark is
  typed no first prompt, by `yardr` at the start (`prompt_look` is `no
  screen, not typed`) or by the daemon later: the prompt stays pending, no
  delivery is counted and none is lost, and one `session_harness_starting`
  on the bead (or the crew member's journal) says so, its `tail` the pane's
  last lines. The daemon looks again every ten seconds and types the prompt
  once the input is there. The wait has no limit of its own: a bead's
  session that stays idle is the group's `idle` limit's (`session_idle`,
  with the pane's last lines again), and the event is in nobody's default
  subscription. A pane that cannot be read, and any other kind, is typed as
  before.
- **How a pane is read.** yardr reads an agent's pane without moving it.
  herdr answers a text read of a pane's recent lines (`herdr pane read
  --source recent` or `recent-unwrapped`, with more `--lines` than the
  screen has) of an idle agent that runs on the terminal's alternate screen,
  as Claude Code in fullscreen does, by scrolling the agent's transcript
  with its mouse wheel and back: the pane moves under whoever looks at it
  (herdr's docs, "Alternate-screen history reads"). A read with `--format
  ansi` and a read of `--source visible` move nothing. Every read the daemon
  makes is `--source recent-unwrapped --lines <n> --format ansi`, once per
  pane and pass, and each check reads from that one snapshot; the
  folder-trust dialog is read with `--source visible --format ansi`. So are
  the reads of `yardr chat` and of `yardr start`.

  What such a read gives depends on the harness. One that prints to the
  normal screen has its transcript in herdr's scrollback, and the read is its last lines, up to
  200. One on the alternate screen has no scrollback, and the read is its
  screen and no more. Every check works on the screen alone: the input, a
  dialog and a login screen are drawn at its bottom. What can leave it is
  told as not known, never as a fault:

  | what is looked for | when it has left the screen |
  |---|---|
  | the first prompt, above the input | herdr saw the agent start to work on it: arrived, `session_prompted` with `unseen`. herdr saw nothing start: lost, and delivered again, as a prompt that never arrived is |
  | the first prompt, in a read that is 200 lines long | not known, as a pane that cannot be read: what was sent is taken as arrived |
  | the pane's last lines, for `tail` on an event (20 asked for) | the lines the screen has, which may be fewer; nothing is fetched for the rest |

  One read fetches history, and herdr may scroll the pane for it: `yardr
  start` and `yardr init --start`, while they wait for the yardmaster, read
  its pane's history when the pane's screen does not show its first prompt,
  to see whether it was answered. A person runs those and waits on them; the
  daemon never reads so. For an agent's history by hand: `herdr agent read
  <agent> --source recent-unwrapped --lines <n>`.
- **Decide what agents may run unattended.** Anything outside `args`'
  permissions blocks the agent on a prompt. Pass what the role needs, e.g.
  `--set 'args=["--permission-mode","acceptEdits","--allowedTools","Bash(yardr:*),Bash(git:*)"]'`;
  compound shell commands still prompt unless allowed or auto mode is on.
- **Blocked agents are reported.** When herdr sees an agent waiting on a
  prompt, yardr writes a `session_blocked` event (visible in `yardr watch`);
  answer it in herdr and the session carries on.
- **Worktrees are cleaned up when the bead closes**, not when a stage ends,
  so later stages reuse them. A worktree with uncommitted or untracked changes
  is kept (a `workspace_kept` event says why) and `yard/<bead>` branches are
  never deleted. Set `--set cleanup=false` on a group to keep its worktrees.
  Reopening a bead checks its branch out again.
- **Labels.** herdr nests each workspace under its depot's node, so labels
  leave the depot out: a bead's workspace is `<group> [<train>] <bead>` (the
  train only for a child of one) and its stage's tab `<stage> [<train>]
  <bead>`, with `yardr-` trimmed from `yardr-sv6b`: `builders [sv6b] p4cv`
  and `new [sv6b] p4cv`, or `builders cbdc` and `new cbdc`. An id that does
  not start with `<depot>-` stays whole, and ids everywhere else in yardr stay
  full. The group appears as its `label` config when set (`--set
  label=codex` on `yardr-reviewers` gives `codex [sv6b] p4cv`), else as
  its name without the `<depot>-` prefix. Crew members' workspaces are labelled
  with the crew member's name.
- The daemon subscribes to herdr's event stream, so an agent that exits is
  noticed within a second; the reconcile timer remains the fallback.

## Example: a crew member reviews the beads with a label

A crew member, or one person, can be the reviewer of some beads while the
depot's reviewers keep the rest. No command assigns one bead to one
reviewer; a label on the bead and a route for that label do it. The case
here is from the yard that builds yardr: the crew member `brakeman`, a
second opinion on another model, reviews the beads labelled
`brakeman-review`.

```sh
yardr group add brakeman-reviews --runner manual --members brakeman,yardmaster --limit 3
yardr route add --stage review --depot yardr --label brakeman-review --priority 10 --group brakeman-reviews
yardr subscribe brakeman yardr --where stage=review --where label=brakeman-review
yardr bead update <id> --add-label brakeman-review        # per bead, before it reaches review
```

What each piece is for:

- **The group** holds the bead while it is reviewed. Its runner is `manual`,
  so nothing is started: the bead waits there until a member moves it.
  `--members` are the only ones who may: anyone else is refused (`only
  members of group brakeman-reviews may move it out of stage review`). Who
  asks is told by the pane before the name: a command run in a crew
  member's pane is that crew member's whatever `--actor` it names, and one
  from a bead's session is that agent's, which is a member of no group. A
  command from any other shell goes by the name it gives, so the check
  stops a mistake and is no wall
  ([peers.md](peers.md#who-looks-at-a-bead-first)). The
  yardmaster is a member so that it can move a bead the crew member leaves
  lying. `--limit 3` is how many beads the group holds at once; a fourth
  waits in review, unclaimed, until one leaves.
- **The route** sends the depot's beads with the label to that group when
  they are in `review`. `--priority 10` puts it before the depot's own route
  for `review`, which has priority 0. A bead without the label matches only
  that other route and goes to the depot's reviewers as before.
- **The subscription** is what tells the crew member. A manual group does
  not nudge its members when a bead arrives: `--set notify=true` on the
  group shows a herdr notification, which is for a person at the terminal
  and reaches no crew member. With the subscription the move into `review`
  lands in the crew member's inbox and the daemon nudges it (`yardr: 1 new
  in your inbox (advanced)`).
- **The label** picks the beads, one `bead update` each.
- **The crew member's role** needs a section that says what a review is and
  how it ends, since a manual group gives no brief: read the bead and its
  outcomes with `yardr prime --bead <id>`, read the diff, commit nothing,
  and report from its own pane with `yardr bead done <id> --note -`,
  `--outcome changes` or `--outcome question`, as the flow's `review` stage
  names them. A changed role reaches a running crew member at its next
  session (`yardr crew restart <name>`, which costs it its context) or by
  mail.

On the first bead reviewed this way the crew member ran `yardr bead done
<id> --note -` in its pane; the bead went to `approved`, and the assembly
tested and merged it within a minute. The assembly asked for nothing from the
reviewer: the builder's test line named the branch's last commit, and the
reviewer had committed nothing.

The limits:

- **The label must be on before the bead enters review.** Routing and the
  subscription both act on the move into the stage. A bead labelled later
  stays with the group that has it and the crew member hears nothing. `yardr
  bead release <id>` has it routed again, by its labels as they are now;
  the crew member is still not told, so mail it.
- **A manual group raises nothing while a bead waits in it unless it sets
  `wait`.** No session stalls or goes idle there. With `yardr group set
  brakeman-reviews wait=30m` a review that has waited that long raises
  `waited`, once, which the yardmaster hears ([A bead that waits
  long](#a-bead-that-waits-long)); `yardr review list -g brakeman-reviews`
  shows how long each bead has waited either way.
- **When the crew member goes, the group, route and subscription go too.**
  `yardr group rm brakeman-reviews` deletes the group with its routes and
  releases the beads it holds, which are then routed again (a bead that
  still has the label goes to the depot's reviewers), and `yardr
  unsubscribe brakeman yardr --where stage=review --where
  label=brakeman-review` removes the subscription. The same release happens
  when a group is deleted to be made again under another name or runner.
- **One route per depot.** The route names one depot; each further depot
  gets a `route add` of its own to the same group, and a subscription
  without `--where depot=` already covers them all.

For a person in place of the crew member, put their name in `--members`.
The subscription then is a notification in their inbox (`yardr subscribe
<user> yardr …`, see [subscriptions.md](subscriptions.md#users-as-subscribers)),
nothing nudges a person, and [`yardr review`](review.md) walks the beads
that wait on them.

**Topics are labels too.** A label with the prefix `topic:`
(`topic:linear-out`) says what the bead is about, and questions on beads
that share one stand together; a bead's topic is its first such label.
They route like any label. Only a route that names the label exactly
(`--label topic:linear-out`) matches it. A route for `muse` does not match
a bead labelled `topic:muse`. Matches are exact, so the prefix changes no
route's matches.
