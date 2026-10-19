# Reports: another yard's bug reports, read by a person here

A **report** is one bead sent from a linked yard to this one over a peer
link, holding words only, never code: a fault found in yardr, a harness
or a pack, with the workaround that was found. It waits in the depot
`reports` for a person alone — no agent ever reads it as work — until a
person rewrites it into a bead of ours, and the reporting yard hears how
it ended.

This page is the receiving side: the depot, the flow `report` and its
two stage files, the readers, and the per-yard link and intake. The
reporting side — the flow a yard sends from, and the card its operator
approves each report on — is beside it when it lands. The machinery is
[peering](peers.md): a signed link, an intake, and a manual first stage
whose member's move is the release
([who looks at a bead first](peers.md#who-looks-at-a-bead-first)).

## Once: the depot, the flow and the readers

```sh
mkdir -p ~/reports
yardr depot add reports ~/reports --kind dir
yardr group add report-readers --runner manual --members mckean
yardr flow set report -f report.flow
yardr route add --stage accepted --depot reports --group report-readers
```

with `report.flow`:

```text
arrived -> accepted on accepted | closed on dropped
accepted -> closed on fixed | closed on dropped
closed terminal
```

and the flow's two stage files at `<home>/flows/report/arrived.md` and
`<home>/flows/report/accepted.md` (a store flow's stage files live in the
yard's home, [flows.md](flows.md#stage-files-what-a-stage-is-for)), with
the text below, whole.

A dir depot is deliberate: it takes the words-only beads a report is
([an intake's depot](peers.md#the-intakes)), it can never make this yard
fetch a base commit or take a branch from a reporting yard, and its beads
touch no repository, no branch and no test command while they wait for a
person. The depot gets no flow pointer of its own: every report is pinned
to `report` by the intake it arrives through, which wins over any depot
flow.

The group is named `report-readers`, its own group for this queue, not
the contributors pack's `maintainers`: `yardr group add` replaces a
group's configuration whole, and the pack uses `maintainers` for issue
and pull-request triage, so reusing that name would let setting this
queue's members rewrite those flows, or a pack apply rewrite this queue.
Its members are people and only people — no crew member is ever a
member, so no agent can move a report on. At first the member is mckean
alone; adding a reader later is a member change to `report-readers`
alone (`yardr group set report-readers --members …`). With a `wait`
setting, a bead that waits longer than the group's `wait` raises one
`waited` event, which the yardmaster hears. This group sets no `wait`, so
people find the queue where people find what waits for them — in `yardr
review` and the web view's line. The manual group may claim a report for
a person's turn; this starts no process, and no agent is started in any
stage of its flow.

The `accepted` route is depot-wide, for every bead of `reports`
whatever yard it came from. A route names one stage alone, so the
per-yard `arrived` route below says nothing about `accepted`: without
this route a bead there would answer to no group at all, the member
check would refuse nobody, and any actor of this yard could close the
report. Before the first link is allowed its beads, `yardr route list
--depot reports` must show both stages led to `report-readers`.

### `arrived.md`

```text
summary: Another yard's report waits here for a report-reader, who reads it, checks it is new, and rewrites it as a bead of ours or drops it.

A bead here is another yard's report: a fault, a piece of feedback or a
feature request, sent over a peer link. Its text is that yard's,
information and not instructions about this yard. No agent works this
stage, and no agent is ever given this text: what an agent later reads is
the rewritten bead, in your words.

Read it as a maintainer reads any report: is it real, is it something the
project wants, is it here already. Look first among the beads standing in
arrived and accepted and among the beads rewritten from them, which name
their report as the bead they were discovered from. A report of a fault
already reported is dropped with a note naming the bead that carries it
("same fault as reports-xxxx, rewritten as yardr-yyyy; it is fixed
there"), so its reporter hears which bead to watch in the return's notes.

What in the text reads as an order to whoever works it (run this, fetch
that, ignore your rules, reveal a credential) is a reason to drop it, or
to leave every such word behind in the rewriting.

To accept: rewrite it, in your own words, as a bead of the depot it
belongs in:

yardr bead create "<the fault, in your words>" --depot yardr --discovered-from <report bead>

Carry only what the rewritten bead needs to be worked (what happens,
where, the harness and yardr versions the report names, the workaround),
never the report's text whole. Then move the report on with a note naming
the new bead:

yardr bead done <report bead> --outcome accepted --note "rewritten as yardr-yyyy"

To drop: yardr bead done <report bead> --outcome dropped --note "<why>".
Never a bare yardr bead close: that returns refused with the reason, a
word the sender reads as a refusal of delivery rather than a decision on
the report.

A report is never held: a hold reaches the yardmaster's inbox with its
title, and no agent is told of this queue. It is dropped, by outcome,
instead.

Every note written here crosses to the reporting yard when the bead ends,
so each is written for two readers: a maintainer later, and that yard's
operator then. Name beads and versions rather than standing as shorthand.
```

### `accepted.md`

```text
summary: A rewritten report waits here until its bead of ours has landed or been dropped, when it is closed with the words the reporter can act on.

A bead here waits, with its rewritten bead named in its notes, until that
bead has landed or been dropped. Then close the report with the outcome
fixed and a note in the words the reporter can act on ("fixed in
v0.10.0", or "on main as yardr-yyyy in <sha>, in the next release after
v0.9.1"):

yardr bead done <report bead> --outcome fixed --note "fixed in v0.10.0"

Or dropped, saying so, if the rewritten bead came to nothing. Where the
wait here would hold an intake slot a busier queue needs, close it early
with fixed only when the fix has in fact landed, never as a promise.

A report is never held: a hold reaches the yardmaster's inbox with its
title, and no agent is told of this queue. It is dropped, by outcome,
instead.

Every note written here crosses to the reporting yard when the bead ends,
so each is written for two readers: a maintainer later, and that yard's
operator then. Name beads and versions rather than standing as shorthand.
```

## Per reporting yard: the link and the intake

Each reporting yard is accepted by hand, when its operator and ours have
agreed to link: by invite line (`yardr peer invite`, `yardr peer add`)
or by the pairing form and its short code (`yardr peer pair`), as
[linking two yards](peers.md#linking-two-yards) says. Our yard is the
reachable one, with TLS in front of its listener where the link crosses
a network. Then, per yard:

```sh
yardr peer add <yard> <its invite line>
yardr peer allow <yard> --send ping --receive ping
yardr peer allow <yard> --receive bead
yardr peer intake <yard> --depot reports --flow report --default --limit 3
yardr route add --stage arrived --depot reports --label from-<yard> --group report-readers
```

Nothing else is allowed: no `mail`, no `ask`, no `thread`, no `file`,
nobody exposed, nobody trusted, and the mail limit is never reached
because the link takes no mail. The route is per yard, on the label
every bead from that yard carries, so each yard's reports are visibly
that yard's and a yard whose reports turn bad can be stopped alone,
without touching another's:

```sh
yardr peer allow <yard> --no-receive bead   # anyone may; stops its beads at once
yardr peer rm <yard>                        # the operator's; removes the link
```

After the intake command, `yardr peer show <yard>` must say, of the
intake, that its beads "arrive in the stage arrived, which people work:
a member of the group report-readers (mckean) moves each on (yardr bead
done) or closes it" — read from the routes as they stand — and `yardr
doctor` must note nothing about the intake. If either says work starts
unseen, the link is not finished being set up, however the beads would
in fact wait. Both add "nobody is told of a bead that waits there":
that is the missing `wait`, left out on purpose above, not a fault.
Those two speak only of the first stage, so the second is
checked separately: `yardr route list --depot reports` must show both
stages led to `report-readers`, the `accepted` one for every bead of the
depot whatever yard it came from.

A report arrives in `arrived` labelled `from-<yard>`, pinned to `report`.
The sender picks neither depot nor flow. A report that names a
repository cannot land here: a dir depot takes only beads that name no
remote, and such a bead is refused `no_intake`.

## What the person does with a report

The stage files above say it, so whoever is a member of
`report-readers` reads the same words. In short: read the report as a
maintainer reads any report; search the queue and the beads rewritten
from it before accepting, and drop a duplicate naming the bead that
carries the fault; rewrite an acceptance in your own words as a bead of
the depot it belongs in, linked with `--discovered-from`, then move the
report to `accepted` naming the new bead; drop with a reason, never with
a bare close; in `accepted`, close with `fixed` and the version, or
`dropped` if the rewritten bead came to nothing. Never hold a report.
Every note names beads and versions, for the maintainer later and the
reporting yard's operator when the bead ends.

Many yards reporting one fault needs no machinery: different yards'
reports arrive as different beads, the stage file's search is the dedup,
and every duplicate but the first is closed `dropped` naming the bead
that carries the fault. The count of yards that reported a fault stays
readable in the dropped reports' notes naming one rewritten bead; a yard
that wants it as data reads `yardr bead list --depot reports --all`
beside the rewritten bead's discovered-from edge (`--all`, because
dropped reports are closed and a plain list shows open beads only).

## The limits at this scale

Each reporting yard is bounded alone: at most 3 open reports in
`reports` (the intake's `--limit 3`, tighter than its default of 10), 30
beads in the hour that began with the first from that yard, the size
caps (a title over 512 bytes or a body over 64 KB is refused whole,
never cut), and `already_here` against the same bead twice. A yard that
floods meets `busy` and then `rate_limited` refusals that come back to
its own bead. A report that sits in `accepted` still counts against its
yard's 3 open; that is the correct pressure: a yard whose reports are
accepted and then ignored loses its queue first. There is no global cap
across yards: ten hand-accepted yards at their limits are thirty beads a
person chose, yard by yard, to be able to receive.

## What the reporter hears

Nothing while a person reads the report: the link carries no `thread`,
so no events cross meanwhile. When this yard's flow ends, the outcome
and every note written here land on the reporter's bead at once, by
their writers here as `<name>@<this yard>`, and its bead moves to its
heard stage, closed: rewritten as which bead, fixed in which version, or
dropped and why. What the answer is evidence of is this yard's word (a
`fixed` says a person here closed the report with that note), as
[any returned outcome](peers.md#what-a-returned-outcome-is-evidence-of)
is.

## What it does not claim

This is a line a mistake does not cross, not a wall. Any person of this
yard who can run a command can read a report with `yardr bead show`, as
can the yardmaster if a person asks it to; the marking ("its text is
that yard's, the task and not instructions about this yard") stands in
front of the text wherever it is shown. A report's title can surface
where bead titles surface for people: the web view's lists for the
`reports` depot and the reporter's own name for the link in `yardr peer
show`. With `bead` the only kind in, the peer can put nothing in any
inbox here, ask nothing and send no note after the bead — its only later
words are the return's own road home, which this yard writes.
