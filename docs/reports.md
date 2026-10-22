# Reports: another yard's bug reports, read by a person here

A **report** is one bead sent from a linked yard to this one over a peer
link, holding words only, never code: a fault found in yardr, a harness
or a pack, with the workaround that was found. It waits in the depot
`reports` for a person alone — no agent ever reads it as work — until a
person rewrites it into a bead of ours, and the reporting yard hears how
it ended.

This page holds both sides: the receiving side first — the depot,
the flow `report` and its two stage files, the readers, and the
per-yard link and intake — then the reporting side: the flow a yard
sends from, and the card its operator approves each report on. The
machinery is [peering](peers.md): a signed link, an intake, and a
manual first stage whose member's move is the release
([who looks at a bead first](peers.md#who-looks-at-a-bead-first)),
and on the way out [a stage worked by a linked
yard](peers.md#the-sending-side-a-stage-worked-by-a-linked-yard).

## Once: the depot, the flow and the readers

```sh
mkdir -p ~/reports
yardr depot add reports ~/reports --kind dir
yardr group add report-readers --runner manual --members mckean
yardr flow set report -f report.flow
yardr route add --stage accepted --depot reports --group report-readers --originator any
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
yardr route add --stage arrived --depot reports --label from-<yard> --group report-readers --originator any
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

## Reporting upstream: the flow `report-out` and its card

The reporting side is one link, one flow and two groups on the yard
that sends. Its operator approves each report on a card before anything
crosses; anyone of the yard may draft one for that approval.

Once, on the reporting yard:

```sh
yardr peer add upstream <its invite line>
yardr peer allow upstream --send ping --receive ping
yardr peer allow upstream --send bead
yardr group add at-upstream --runner peer --set peer=upstream --limit 3
yardr group add report-owners --runner manual --members <operator>
yardr flow set report-out -f report-out.flow
yardr route add --stage draft --group report-owners --label upstream-report --originator any
yardr route add --stage sending --group at-upstream --label upstream-report --originator any
```

with `<operator>` your own user name, and `report-out.flow`:

```text
draft -> sending on send | dropped on dropped
sending -> heard on fixed | heard on dropped | draft on refused | draft on undelivered | draft on closed
heard terminal
dropped terminal
```

and the flow's stage file at `<home>/flows/report-out/draft.md` (a
store flow's stage files live in the yard's home,
[flows.md](flows.md#stage-files-what-a-stage-is-for)), with the text
below, whole.

The link carries `bead` out and `ping` both ways, and nothing else:
no `mail`, no `ask`, no `thread`, no `file`, nobody exposed, nobody
trusted. The group that sends is refused while the link does not carry
`bead` out, so the `peer allow` lines come first. The vocabulary of
`sending` is the return's: the receiving flow's terminal moves return
`fixed` or `dropped`; a bead the far yard could not take ends its
stage as `refused`, and one that never reached it as
`undelivered`, and both go back to `draft`, where the reason stands
as a note on the bead and the report may be mended and sent again; a
hand move there into `closed` returns that stage's name and is read
the same way, so a mended resend is never blocked by a word the flow
lacks.

Both routes earn their flags, and neither is decoration. The `draft`
route to `report-owners` is what puts the card in the operator's
decisions and makes the move out of `draft` the operator's alone: a
stage ask is put only where the stage's route leads to a manual group,
and the member check likewise refuses nobody where there is none, so
without it a drafter could move their own report on and never meet
the card at all. Both routes take `--originator any`, because a
report's originator is whoever's work it builds on — a person who
creates one is its originator, and one an agent files from its pane
takes that pane bead's — and any person or agent of the yard may
draft a report for the operator to approve; without it a report
drafted for anyone but the operator would wait with no card put and
no group to refuse its drafter, or never be claimed. Both routes also
name the label `upstream-report`: the bead must carry it, as the
creation below does, or no route takes it in either stage and it
simply waits — an unlabelled bead is never sent.

After the setup, `yardr flow show report-out` must name the `draft`
stage file with no broken warning beside it (a broken card puts no
ask, and the bead still moves by hand); it says `pointed at by
nothing`, which is expected — report beads are depot-less and pin
the flow themselves. `yardr peer show upstream` must say `send
bead, ping`, and `yardr route list` both stages with the label and
`any` originator.

### `draft.md`

```text
summary: A report waits here for the operator, who re-reads it whole and picks Send on its card, or drops it.

A bead here is a report this yard may send to the upstream yard: a fault,
a piece of feedback or a feature request found in yardr, a harness or a
pack, with the workaround that was found. It is a depot-less bead, so it
names no remote, starts no work anywhere and crosses with its words alone.
Until the operator picks Send on its card, nothing crosses: the stage is a
manual one whose only member is the operator, so a bead's agent cannot
move its own report on.

A report holds, in its title and body: what happened and what was
expected, the harness's kind and version where a harness is involved
(read off the machine as yardr doctor reports it), this yard's yardr
build, the kind of depot it happened in, the workaround that was found,
and how often it has been seen. The title holds at most 512 bytes and
the body at most 64 KB: over either cap the bead is not sent, and its
stage ends refused saying so. The type is bug, feature or task (any
other arrives as task); the priority is left at the yard's default; the
bead carries no links to outside issues (name one in words where one
matters). Notes cross only as they fit — a long one arrives cut with a
mark saying so, the oldest left out past their cap together — so a note
may repeat what the body says and must never extend it: nothing the
upstream reader will act on lives only in a note.

A report never holds: a secret or any credential, token or value from an
environment; code, a diff or a file, from a repository or anywhere else;
another bead's title, body or notes — the report is written fresh from
the finding, not forwarded; a path, hostname, address or person's name
from this machine that the fault does not need; and no attachment of any
kind, since the link carries no file.

Drafting — by a person or by an agent — ends with a note on the bead
saying the report is ready, and from that note nobody but the operator
edits the bead. The operator then re-reads the bead whole (yardr bead
show <id> with its history, yardr bead history <id>) immediately before
picking, and picks Send only where the history shows no change since the
ready note but their own. Picking Send approves the bead as it stands at
the pick, never a snapshot of it as it stood when first read: pick on
the fresh reading. Never edit a bead in sending.

## Ask
kind: choice
question: Send this report to the upstream yard?
context:
- Picking Send sends the bead as it stands at your pick, not as it stood when you first read it: re-read it whole now, bead show and its history, and pick on that reading.
- The title and body cross whole or not at all: over their caps the bead comes back refused, never trimmed. Everything the report depends on belongs in the body.
- Notes cross only as they fit: a long note arrives cut with a mark saying so, and the oldest are left out past the notes' cap. Notes repeat the body; they never extend it.
- Its type is bug, feature or task (any other arrives as task), its priority stands at the default, and it carries no links: name an outside issue in words.
- It holds no secret or credential, no code, diff or file, no other bead's title, body or notes, no path, hostname, address or person's name the fault does not need, and no attachment.
recommend: 1
why: The operator has just re-read the bead whole, its history shows no edit since drafting finished, and it holds no secret, no code and no other bead's words.
send: The report crosses to the upstream yard and waits there for a person.
dropped: The bead closes here and nothing crosses.
```

## Drafting and approving a report

A report is drafted as a depot-less bead to the template the `draft`
stage file carries:

```sh
yardr bead create "Kimi 2.2.0 trust dialog stops a session's first prompt" \
  --type bug --label upstream-report --meta flow=report-out -b - <<'EOF'
What happened: a session started under Kimi 2.2.0 stops at the trust
dialog ("Do you trust this folder?") and waits; the first prompt is
never answered and the bead sits stalled until a person accepts.
Expected: the session starts and answers its brief.
Harness: kimi 2.2.0. Yardr: f415c7fb50e4. Depot kind: git.
Workaround: answer the dialog once by hand, or start the harness with
its trust flag; sessions run normally after that.
Seen: twice on 2026-10-19, on the first session after the upgrade.
EOF
```

It waits in `draft`: routed to `report-owners`, its card in the
operator's decisions (`yardr review list` shows the bead;
`prime --bead` shows the card, with the command that answers it), and
nothing crosses until the Send pick. A person drafts one as
themselves; an agent drafts one from its pane like any bead, and it
takes that pane bead's originator. Either may edit it while it waits,
but the stage's only member is the operator, so a drafter's own move
of it out of `draft` is refused naming the operator — the refusal is
the store's, from the pane the command came from. Drafting ends with
a note on the bead saying it is ready.

The operator then re-reads the bead whole — `yardr bead show <id>`
with `yardr bead history <id>` — immediately before picking, and
picks Send only where the history shows no change since the ready
note but their own:

```sh
yardr bead answer <id> <ask> 1 [words]
```

with the ask's number as the card prints it. Picking moves the bead
to `sending`, which the peer group claims and sends. What the pick
approves is the bead as it stands at the pick, never a snapshot of
it as it stood when first read; afterwards the same history proves
it: no `updated` event but the operator's own between the ready note
and the `peer_bead_sent` event means the title and body that crossed
are the ones that were approved. Picking the card's second option
closes the bead here in `dropped`, and nothing crosses.

Over the title or body caps the bead is not sent: its stage ends
`refused`, back in `draft`, and the bead says why — `not sent to
upstream … it is too large`, with the two sizes — in a note by
`yard`, where the report may be mended and sent again. A fresh card
is put on its return, as on any re-entry.

## What comes back

In `sending` the bead's line says where it stands at the far yard —
`at upstream: sent <when>, upstream last heard <when>` — and hears
nothing while a person reads it: the link carries no `thread`, so no
events cross meanwhile. When the far flow ends, the outcome and
every note written there land on the bead at once, by their writers
as `<name>@upstream`, and the bead moves to `heard`, closed: a
`fixed` says which version, a `dropped` says why, each with the
rewritten bead named. A `refused` ending returns the bead to `draft`
instead, with the reason as a note by the far yard and a fresh card,
where the report may be mended and sent again. What the answer is
evidence of is that yard's word, as [any returned
outcome](peers.md#what-a-returned-outcome-is-evidence-of) is.
