# Observing the yard: a signal panel, and the calm home beside it

Design yardr-8y0f, proposed 2026-10-12. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

Design for bead yardr-8y0f (flow `design`, stage `draft`). The web view is
`yardr web serve`, the read-only browser view of the yard (docs/web.md); its
home is the page at `/`. A yard is one installation of yardr; a depot is a
git repository or a plain directory that beads are worked in; a bead is one
durable piece of work; a session is one agent working one bead in one stage;
a flow is the stages a bead goes through and a stage is one step of it; a
group is who works a stage and a route is which group takes a stage; the
operator is the person who runs the yard. The feed is the home page's list
of what happened, newest first; the line is the right column, what waits for
a person and the yard in numbers. An ask is one question as data, the new
thing design footplate-xrgz proposes and beads yardr-jbpa and yardr-qzag
build. See docs/glossary.md for all of yardr's words.

## 1. The question

mckean, 12 Oct 2026, in the yardmaster's pane: "i just checked the web view
on mobile, still overloaded... i can collect examples of less busy design,
maybe you can too". After the yardmaster's reading and list of calmer
screens: "about the design you're right, good list, let's not wait for me
but file as a design bead with your sets and my input". "Still" because an
earlier cleanup did not get there. mckean, shown the yardmaster's set:
"about the design I think a simple version to show the flow with stages
not sure what you call this ui but they had it in train operating
rooms", with a picture of a signal box's control desk. mckean, on the
yardmaster's reading of that as a header for home: "the overal look
sure, but I was thinking of usign this kind of diagram to observe a
yard with it's flows and where work is happening, where it's blocked
etc." And on the yardmaster's set and findings in the body: "your
general input was also good, keep it!". So both stand, neither replaces
the other: what does the home page of `yardr web serve` show on a phone,
390 px wide, so that a person sees at once whether anything needs them,
and finds the rest one tap away — and how is the whole yard observed on
one panel, every depot's flows drawn as they really are, with where
work happens and where it is blocked, simple?

## 2. What exists today

**The view and its promise.** `yardr web serve` listens on
http://127.0.0.1:8791/ and answers every route to a browser as HTML and to
a JSON client as the same data (docs/web.md, "Pages"). It reads only: no
page takes a write and every route refuses any method but GET and HEAD. Its
JSON is the yard's read API and other clients read it, so this design moves
things on the page and leaves the JSON whole. There is one stylesheet,
`internal/web/assets/yard.css`, and no build step, no web font, no other
host. A test fails on a URL naming another host and every response carries
`Content-Security-Policy: default-src 'self'`.

**Home's data.** `Home` in `internal/web/home.go` is the overview above the
feed: `Attention` (what waits for a person), `Running` (the sessions
running now), `Depots` (per-depot open beads counted per stage), `Closed`
(what closed today), and the `Feed`. `overview()` counts the depots;
`handleHome` reads the feed first, then the overview, then the line, then
fills `Attention` from the line's `Waits` plus the line's `Parked` except
beads parked where they start (`firstStage`: a backlog waits on no one in
particular), fills `Running` from the live sessions except manual groups'
claims, and fills `Closed` from `closedToday` (at most `closedShown`, ten,
newest first). The overview is a snapshot: reload the page to see it
change.

**The line.** `Line` in `internal/web/waits.go` is the right column, the
same on every page, and `/line` as a page of its own where the window has
no room for the column. `waits()` lists the open beads no agent will move:
`Waits` are those something stops (held, blocked on a prompt, stranded in
a stage their flow lacks) and `Parked` are those whose only reason is a
stage of a manual group, each with every reason in `why` (`held`,
`blocked`, `stranded`, `manual`). `Alarm()` is true for a hold and a
blocked prompt: something gone wrong, not a stage that is a person's by
design. Under them the line in numbers: `open`, `working`, `held`,
`sessions`, open per stage and per depot. The column is live: it reads
`/line` again on every event of the stream.

**Giving up.** A builder that gives up is a hold with a louder reason.
After `MaxAttempts` failures in one stage the dispatcher holds the bead
and then logs a `gave_up` event with the stage, the failure count and a
hint (`internal/dispatch/dispatch.go`, `SetHold` then `LogDispatch`), so
the bead already stands in the line as held, with the generic held
reason (`waits.go`, `held()`, which reads the note left before the
hold). A crew member that fails to start `MaxStartFails` times in a row
is parked and logs `crew_gave_up` (`internal/store/crew.go`,
`CrewStartFailed`): that is a crew event, not a bead's, so no bead waits
because of it.

**The feed.** `Feed` in `internal/web/feed.go` is one page of what
happened, newest first, `feedPage` (50) entries; `/?before=<seq>` is the
next page and htmx follows it on scroll. `inFeed()` leaves out only a crew
member's journals and handoffs, most `crew_status`, `mail_read`, timer
bookkeeping and timer mail; everything else has an entry. `entry()` says
each event in the yard's words (docs/web.md, "The feed"), with `note` cut
at `maxNote` (280 bytes). The first page is live: each event's fragment
goes on top over the stream.

**The shell and the look.** `internal/web/templates/shell.html` lays out
menu, centre (up to 600 px) and right (300 px); `home.html` renders the
overview sections in order (Attention, Running, Depots, Closed) then the
feed; `aside.html` renders the right column (`Waits for you`, `Parked`,
`The line`); `entry.html` renders one feed entry as a four-line card
(actor and time, what happened, full title, note); `line.html` renders the
column in the centre. `yard.css` sets monospace (`--mono`) as the main
face at 14 px (`--text`), 11 px uppercase labels (`--small`), 20 px names
(`--large`), with `--sans` used only for longer prose (`.item > p`,
`.card-title`, `.panel p`). At 700 to 1099 px the right column goes and a
Line entry appears in the menu; under 700 px (a phone) the centre column
stands alone and the menu is a bar at the bottom, Line included, which
stays in view while the page scrolls. One stream serves the page
(`shell.html`, `sse-connect="/events?after=..."` with `Page.stream` as
its `?format=`, `""` for the feed's entries): the aside re-reads `/line`
on every message, and home's feed swaps each message's fragment on top;
an event the feed leaves out passes as its id alone (`events.go`,
`frame()`), so the client's cursor still passes it.

**The live view, checked 12 Oct.** Read-only through `curl`, HTML stripped
to text, plus the JSON. Home holds, in order: "Waits for a person" (one
held bead with its full three-line title), "Running now" (two sessions),
"Depots" (five depots, each with its flows as stage-count pairs), "Closed
today" (nine full titles with times), then the feed (50 entries). The
feed's kinds in that page: `dep_added` 8, `started` 5, `claimed` 5,
`advanced` 5, `created` 5, `subscribed` 4, `crew_started` 4,
`crew_stopped` 4, `workspace_removed` 2, `noted` 2,
`crew_rotation_asked` 2, and one each of `closed`, `session_prompted`,
`daemon_replaced`, `server_replaced`. A routine crew restart
(`crew_started` / `crew_stopped`, reason `restart: handoff`) stands as two
four-line cards. `/line` holds one wait and 23 parked, almost all in
`backlog`.

**The yardmaster's reading, checked.** Three of its four points hold: the
feed is mostly noise as counted above; everything has nearly one weight
(monospace 14 px, full titles, the 11 px labels and 20 px names doing
little); "Closed today" lists full titles where a count would do. The
first point needs two corrections. Its list omits "Running now", which
stands between the waits and the depots whenever a session runs. And on a
phone "Waits for you" and "The line" do not stand at the bottom of home:
the stylesheet hides the right column under 1100 px, so on a phone they
are one tap away on the Line page, not below the feed. The duplication is
real but it is between home's "Waits for a person" at the top and the
Line page behind the tab, not top and bottom of one scroll.

**Calmer screens.** The bead names five from memory and asks for the real
screens. Their public shapes agree on the point: Linear mobile's Inbox is
one list with one line per item and the detail behind a tap; GitHub
Mobile's Home is a short needs-you list on top with everything else a row
with a count; Things 3's Today asks one question per screen with air and
shows nothing finished; Vercel's mobile dashboard is one status line per
project with a dot for state and no sentence; Apple Wallet and the Fitness
summary are a few cards with one number each. What they share is the
question this design answers: the first screen says whether anything
needs the person, and the rest is one tap away. mckean's own input
arrived while this was drafted — the signal-box picture (see "The
picture" below), the correction that the diagram observes the yard
rather than heading home, and "your general input was also good, keep
it!" — and at decide his input counts for more than this set.

**The picture.** mckean's input names the shape: a signal box's control
desk, a dark panel with the track layout drawn as thin lines, a few lit
red or green, small blocks standing on the lines where trains are, and
rows of small lamps beside it. The yardmaster's name for it: an
illuminated track diagram, also called a mimic panel or mimic diagram;
in a signal box an NX panel, in German a Gleisbild, the panel of a
Gleisbildstellwerk. What the web borrows is only the grammar: lines for
the way, blocks for what stands on it, colour as the only signal. It
counts for more than the set above; the Linear and GitHub lists still
answer "what needs me" on home beside it.

**The flow's shape, already read.** `overview()` in
`internal/web/home.go` builds, per depot and flow, `HomeFlow.Stages` in
the flow's own order with terminal stages left out (a terminal stage
closes the bead that enters it, so no bead ever stands in one), and
counts each stage's open beads; `Present()` keeps the stages that hold
one. `card()` in `internal/web/beads.go` says per bead whether it is
held and whether a session works it (`working()`), and `Attention`
says which beads wait for a person and why. On `/flows` every stage's
cards stand under an element id (`FlowStage.Section`,
`domID("at", depot, type, stage)`, as the `id` in
`templates/flows.html`), so a section of the diagram can link to the
stage it draws. Anything the panel needs beyond today's JSON — which
beads stand in a stage, and in what state — is computable in the
handler from the yard it already read once, in a struct whose fields
are all `json:"-"`, as `Home.yard`, `Card.Section` and `Card.Live`
already are: the JSON stays byte-identical.

**The signalling diagram, already drawn.** `internal/flowdraw` draws a
flow as a signalling diagram in SVG: the track runs left to right past
a platform per stage, a stage only a person moves a bead out of is a
siding above the line, a transition the track does not carry is a line
below it, and the group that works a stage stands under its platform
(`internal/flowdraw/flowdraw.go`, package comment). `yardr flow show
<depot> --svg` draws the depot's flow so (`internal/cli/flows.go`,
`depotDiagram`), and `yardr flow draw` says how to read the drawing.
Its palette and faces are custom properties with a second set under
`prefers-color-scheme`, so one file reads on a light page and a dark
one, scoped under `.flowdraw`. The drawing is of the flow, not of the
yard: no bead is on it. The panel this design proposes is that track
with the yard on it.

**The flows as text.** `yardr flow show <depot>` lists every flow that
governs the depot with, per stage, the group its routes put there, the
open beads, and the ways on (`internal/cli/flows.go`, `flowViews`).
Depot yardr has four: `default`, `design` (type `design`),
`yardr.train` (type `train`) and `type/wagon` (type `wagon`); the
design flow's edges are `backlog -> draft | decide`, `draft ->
critique on done | decide on question | backlog`, `decide -> approved
| draft | backlog`, `critique -> decide on done | draft on changes`,
`approved -> merged on done | draft on failed | decide on question`,
`merged terminal`. A stage no route serves is shown with no group and a
warning ("stage merge has no route: beads there wait for a human"); on
the web it is a `FlowStage` with `Group ""` (`internal/web/beads.go`,
`flows()`).

**Waiting on a dependency.** `Bead.BlockedBy` names the beads that must
close before a bead is ready, and `OpenBlockers` lists the handles that
hold it now: its blockers still unreleased, through the edges that hold
in its stage (`internal/store/graph.go`, `internal/store/beads.go`,
`openBlockers`). A bead with an open blocker waits, and on a named
one.

**Stalled.** An agent that sits idle after its first prompt past the
`stall` limit logs `session_stalled` (`internal/store/configkeys.go`,
`internal/store/events.go`); the yard's last word on a session is read
from its bead's events, as `blocked()` reads `session_blocked` and
`session_unblocked` (`internal/web/waits.go`).

**A landing that failed.** Every move logs `advanced` with `from`,
`to`, `outcome` and group (`internal/store/beads.go`). The `failed`
outcome sends the bead back (`approved -> new on failed` in `default`,
`approved -> draft on failed` in `design`); so does `changes`
(`review -> new on changes`, `critique -> draft on changes`). A bead
whose last move carries one of those outcomes is back because
something failed, and its events say so.

**Trains.** A bead of a train's family carries its train's handle in
`Bead.Train`, derived on reading, and `BeadFilter.Train` lists a
train with its children (`internal/store/beads.go`); a card carries it
too (`Card.Train`). Children standing together is a grouping of blocks
the panel already holds.

**Questions as data.** Design footplate-xrgz (merged 12 Oct,
`docs/design/2026-10-11-footplate-xrgz-decisions-to-pick.md` in the
footplate depot) writes a question as data: an ask is a note of kind `ask`
on any bead, with context lines, options each with its consequence, a
recommendation, and `you decide` added to every ask; the answer is a note
of kind `answer`. A topic is a bead label with the prefix `topic:`. Bead
yardr-jbpa builds asks and answers in yardr now; yardr-qzag serves them
over the socket next. This design's "Waits for you" must become the same
queue seen from the web, not a second idea of it: section 3.6 says how.

**Principles.** mckean's: an existing mechanism before a new one; simple.
The stage adds yardr's own: prefer a mechanism that exists over a new one,
and a general one over a special case. So: query parameters before new
routes (`?all=1` exists on `/beads`), the existing tokens and classes
before new ones, the existing pages (`/flows`, `/beads`, `/sessions`,
`/line`) as the tap targets, and no JSON change anywhere.

## 3. The proposal

Two things, neither replacing the other. The panel observes the yard:
every depot's flows drawn as they really are, on flowdraw's track, with
the beads standing on it as blocks, a section lit while a session runs
in it, and every kind of blocked told apart at a glance. Home stays the
calm screen: one answer, then rows, then a short filtered feed below
the fold, with the panel one tap away. The first screen says in one
large line whether anything needs the person; under it each thing that
does is one row in plain words, faults first; under those one row per
count, each a link, one of them the yard itself; the feed starts below,
filtered to what a person can act on or be glad to see, with the full
feed one tap away. The same HTML serves both widths: what changes on a
wide screen is only the shell around it, and the right column stops
repeating the waits on home. Nothing leaves the JSON: the home feed
filter is one HTML-only function beside `inFeed()`, applied to the
initial rows and to the stream's fragments alike; the panel is one
HTML-only rendering of the flows the page already reads; and every
count still links to the page that lists it whole.

The existing mechanisms carry it: flowdraw's track, platforms, sidings
and lamps for the panel; `Attention` and `Line` for the queue;
`?all=1` for the full feed and `?panel=1` for the panel, after
`/beads`; the stream's `?format=` for the filtered fragments and its
`seq` for a page that reads itself again; the menu pages as tap
targets; the tokens `--sans`, `--small`, `--large`, `--held`,
`--landed` and the dot for weight; and htmx's stream for liveness.
What is new is only: one function beside flowdraw's `SVG` that draws
the same track with the yard on it, the home template's order and row
shapes, one HTML-only feed filter with its link and its stream format,
one-line ellipsis in the stylesheet, and the aside showing the line
without the waits on home, in its first render and its live re-reads
alike.

A person types nothing new. They open `/` on the phone and read the
answer; they tap a row to open its bead or its page; they tap "All
activity" for the full feed, "Yard" for the panel.

### 3.1. The panel, then the first screen at 390 by 844, three days

**The panel.** One depot-flow is one diagram: flowdraw's track with the
yard on it, drawn by one function beside flowdraw's `SVG` that shares
its plan and canvas. The track is the flow as it really is — the stages
in order, the sidings above for the stages only a person moves a bead
out of, the other edges as lines below — and on it the open beads as
blocks. A section is lit green while a session runs in it; a block is
its bead, and a running block carries the short name of the group that
works it. Tapping a platform opens the stage on `/flows` (its section
anchor); tapping a block opens the bead. No text on the diagram beyond
stage names, group names, counts and the marks below.

The blocked kinds, each told apart at a glance:

- Held: a solid red block. A hold after repeated failures names the
  give-up: a hollow red block (a red ring), because a builder that gave
  up wants fixing where a plain hold may only want reading.
- Stalled: a hollow green block, because the session runs but its last
  word is `session_stalled`. A session blocked on a prompt keeps its
  green block and gains a red ring, because it both runs and waits on
  a person.
- Waiting on a dependency: a blue block followed by the short id of the
  first bead still holding it (its open blockers, `openBlockers`'
  rule), because the panel must say on which one; the id links to that
  bead.
- Back because something failed: a red diamond before the block, where
  the bead's last move carries outcome `failed` or `changes`, because
  a landing or a review that failed is news even though the bead waits
  on no one.
- A stage that waits for a person: a lamp on the platform, red where
  the wait is a fault (held, blocked, stranded, gave up, failed back)
  and blue where it is a person's turn by design (a manual stage, and
  an open ask once yardr-jbpa and yardr-qzag have landed).
- A stage no route serves: a hollow platform with the faint words "no
  route", because beads there wait for a human and flowdraw gives it
  no building.
- Stranded (a stage the flow lacks): no platform can hold the bead, so
  it stands in an "off the line: N" row under the diagram, with the
  bead's stage named.

Blocks stand newest first, as `/flows` cards do; past ten on one
platform the rest fold into "+N", because a backlog of nineteen must
not bury the track, and the count beside the stage name always says
the true number. A train's children stand together on each platform
with their train's short id faint once, because a train travels as
one; the train bead itself stands where its own flow puts it. A bead
of no depot stands on no diagram; the needs list and the rows still
name it. Quiet blocks carry no group name: no one works them.

Drawn in text, depot yardr's design flow on a fault day (the real
edges from section 2; `━━` the track, `──` a back edge below it, `●`
a lit section or lamp, `○` a quiet one, `■` a block, `▣` hollow, `◆`
failed back, `›` a tap; colour named where text cannot show it):

```
yardr · design ›                                          › /flows
backlog siding: ○ backlog · 1 ■
decide siding:  ○ decide · 0
━━● draft · 2 ■(muse) ▣ ━━ ○ critique · 0 ━━ ○ approved · 0 ══▷ merged
── critique → draft (changes) · approved → draft (failed) ──
── draft → backlog · decide → draft · decide → backlog ──
lamps: red on draft (gave up) · blue on backlog (person's turn)
off the line: none
```

The `■(muse)` block runs under group muse-builders; the `▣` block is
a bead held after giving up; the red lamp on draft says a fault waits
there. On screen the track is flowdraw's SVG, the
blocks small rects on the platforms, the lamps its lamp circles in
red and blue, and every platform and block a link.

**Where the panel lives.** It is a page of its own that home leads
to, not home itself: home answers one question ("does anything need
me?") and the panel another ("where is the work?"), and one question
per screen is the calm the body asks for. It lives at `/flows?panel=1`,
a rendering of the flows the page already reads: no new route, the
state it adds computed beside the page's data in `json:"-"` fields as
section 2 says, and the JSON of `/flows` unchanged (the parameter
affects the HTML alone, as `?all=1` on `/beads` lists the closed too).
The menu gains one entry, "Yard", linking there; home's "Yard" row
(section 3.2) is the same tap. The panel reads itself again on every
event of the stream, as `/line` does (`seq`: a page that only reads
itself again), because a signalman watches a live board; a bead that
moves is then on its new platform without a reload, and no card event
is invented.

**Five depots on a phone.** The depots stand stacked, each flow a
diagram under its depot's name, and the page scrolls vertically; each
diagram pans horizontally at full size, because flowdraw's platforms
have a fixed pitch and shrinking them would shrink the names past
reading. One depot at a time would hide the yard the signalman must
see whole, and folding depots shut would hide it behind taps; scroll
hides nothing. A depot-flow with no open bead still draws its bare
track, because the shape of an empty flow is information a missing
flow is not — but its stages carry no counts and no blocks, so the
eye passes over it.

**Home, three days.** Each drawing is the centre column at 390 px
wide, the page head on top and the menu bar at the bottom, in text.
`●` is a status dot, the first `›` in a row is the row's chevron on
screen, `…` a title cut with an ellipsis, and the trailing `› /page`
is an annotation of where the row links, not on screen. Times are this
machine's, as today. The Running and Closed rows show counts alone
here; open questions 2 and 3 settle whether they carry more.

Nothing needs him, two builders run, three closed since morning. The
answer is the whole screen; the feed's header peeks at the bottom edge.

```
Home
──────────────────────────────────────
Nothing needs you.

All clear.

Running 2 ›                        › /sessions
Yard 27 · working 2 ›               › /flows?panel=1
Closed today 3 ›                    › /beads?all=1
──────────────────────────────────────
Just happened                       › All activity
  landed yardr-n8w1 at 04:07 …
──────────────────────────────────────
Home Yard Flows Beads Sessions Crew Line
```

Two things wait for him and four builders run. Each wait is one row:
plain words first, the bead id small at the row's end, stage and age
faint. Faults first.

```
Home
──────────────────────────────────────
2 need you.

● held · Pick the port … · evd6 · 3h
  decide · Name the command … · 41ma · 1d

Running 4 ›                        › /sessions
Yard 29 · working 4 ›               › /flows?panel=1
Closed today 5 ›                    › /beads?all=1
──────────────────────────────────────
Just happened                       › All activity
──────────────────────────────────────
Home Yard Flows Beads Sessions Crew Line
```

A fault with ten things closed since morning. A bead held and a builder
that gave up; the closed are a count, not ten titles. Only the answer
and the two fault rows are large enough to read at a glance; the closed
row is quiet.

```
Home
──────────────────────────────────────
2 need you. 1 fault.

● held · Pick the port … · evd6 · 3h
● gave up · The GitHub pack … · jbpa · 25m

Running 3 ›                        › /sessions
Yard 26 · working 3 · held 1 ›       › /flows?panel=1
Closed today 10 ›                   › /beads?all=1
──────────────────────────────────────
Just happened                       › All activity
  held yardr-evd6 · Pick the port …
──────────────────────────────────────
Home Yard Flows Beads Sessions Crew Line
```

The "gave up" row above is the bead's held row, not a second row: the
dispatcher holds the bead before it logs `gave_up`, so the bead already
waits, and the row's reason names the give-up ("gave up after N failures
in \<stage\>") where the bead's events hold one for the hold the row
names. Counts stay per waiting bead: the answer counts such a bead once,
as a fault, whether its reason is a plain hold or a give-up. A crew that
gave up stands in no needs row, because no bead waits for it, and arrives
as a failure line in the feed instead (section 3.3).

When more wait than fit, the list shows a small number (open question 1
settles how many) and the "All waiting N" row carries the rest to
`/line`, which lists every wait and every parked bead whole. The answer
line counts faults apart ("1 fault") only when one waits; otherwise it
counts needs alone. Rows never wrap: section 3.4 says how a long title
ends.

### 3.2. Every block on today's home

Each block keeps one of four fates. "Stays" means on the first screen;
"row" means one line with a count linking out; "tap" means moved behind
a tap to the page named; "goes" means removed from the page (never from
the JSON).

- "Waits for a person" (`Attention`): stays, as "N need you" with one
  row per wait, because it is the answer the whole page exists to give.
  A held row whose bead's events hold a `gave_up` for that hold names
  the give-up in its reason, because "held" alone hides the fault the
  person must fix; the bead still counts once.
- "Running now": becomes a row ("Running N", open question 2, to
  `/sessions`), because a person acts on who runs nothing and a count
  with the page one tap away reassures as well as names.
- "Depots" (per-depot flows as stage-count pairs): goes, replaced by
  the panel and the "Yard N · working M · held H" row to
  `/flows?panel=1`, because per-stage counts are for observing the yard
  and the panel observes it better, while the row keeps the reassurance
  and the tap on the first screen.
- "The line" (open, working, held, sessions, per stage, per depot):
  goes, replaced by the panel and the "Yard" row above, because the
  numbers are the reassurance and the breakdowns now stand drawn, with
  `/flows` and `/line` each one tap away.
- "Closed today" (up to ten full titles): becomes a row ("Closed today
  N", open question 3, to `/beads?all=1`), because finished work wants
  no action and a count says the day moved, with the titles one tap
  away.
- The feed: stays but filtered and shortened (open question 4 for how
  many lines), with "All activity" to the full feed, because some
  happenings want a person (section 3.3) and the rest want a record,
  not a screen.
- "Waits for you" and "Parked" (the aside, the Line page on a phone):
  the waits stay as the home list above (the same queue, one idea,
  section 3.6); the parked past their flow's first stay in that list
  and the rest live on `/line` behind "All waiting N", because a
  backlog waits on no one in particular and must not fill the first
  screen.
- The page head stays as it is; the menu bar gains one entry, "Yard"
  to `/flows?panel=1`, because the panel is a page of its own and the
  bar is how a phone reaches a page.

### 3.3. The feed: what a person wants on a phone

Kept on home, newest first, one line per event (section 3.4), only:

- `held` and `unheld`: a fault arrives and lifts, the first thing a
  glance must catch.
- `session_blocked` and the return from it: a prompt waits on a person,
  then waits no longer.
- Failures: `gave_up` (a builder that gave up, the bead held with
  it), `session_harness_error`, a session that ends badly
  (`session_<state>`), `crew_gave_up` (a crew that stays stopped): what
  stays stopped is news in a way a restart never is.
- `closed` as landed or by hand, once: the day's progress, the calm
  counterpart to the faults. A landing's `advanced` to the terminal
  stage is hidden beside it, because today one landing stands as two
  entries and a phone should see it once.
- `advanced` into or out of a manual stage, and `advanced` back on
  `failed` or `changes`: a bead arriving where a person works, or
  leaving it, is the queue moving, and a landing or a review that
  failed is news; every other move is agents doing their jobs.
- `noted`, `released`, `reopened`, `timer`: words someone wrote, a bead
  to be routed again, a bead open again, a reminder firing; each is
  rare and each may want a person.
- `asked`, `answered`, put aside, declined: the asks of yardr-jbpa once
  they exist, because a question addressed to the person is the
  first-screen queue's own feed.

Left out of home's feed: `created`, `claimed`, `started`, `updated`,
`dep_added`, `subscribed`, `workspace_removed`, `daemon_replaced`,
`server_replaced`, routine crew `started` and `stopped`, `rotation
asked`, `session prompted` and the dispatcher's other plumbing. Each is
either the yard breathing (a claim, a start, a restart with reason
`restart: handoff`) or bookkeeping whose place is the bead's page.

Where the rest can still be found, each an existing mechanism: "All
activity" on home adds `?all=1`, new on home after the `/beads`
convention (`handleHome` reads only `before` today and ignores `all`),
which renders the full 50 as today and subscribes the stream
unfiltered; `/beads/<id>` history keeps every event of the bead, none
left out; `/events?format=json` keeps every event for machines. The
JSON of `/` keeps the full `events` whatever the HTML shows: the filter
is one HTML-only function beside `inFeed()` — `feed()`, `inFeed()` and
the JSON stay untouched — applied both to the initial rows and, through
a new `?format=` value the home stream subscribes with (`Page.stream`
carries it), to the fragments the stream inserts. An event the home
filter leaves out passes as its id alone, as a left-out event does
today, so the client's cursor still passes it.

Crew starts and stops, the case to settle: routine `crew_started`,
`crew_stopped` and `crew_rotation_asked` leave home's feed. A person can
act on nothing in "crew brakeman started"; what they can act on is a
crew that stays stopped, which arrives as a failure above and as a wait
(`blocked`, `held`) in the needs list. Crew health stays findable on
`/crew` (whether each member runs, since when) and in the full feed
behind "All activity".

### 3.4. Weight: large, small, sans, and how long a title runs

- Large (20 px, `--large`): only the answer line ("Nothing needs you.",
  "2 need you.", "2 need you. 1 fault."). One per screen, like Things:
  the eye lands there first and nowhere competes.
- Small (11 px uppercase, `--small` with `.label`): section names,
  bead ids, stages, times, counts' labels. A bead id is small and at
  the row's end, never the row's text, as footplate-xrgz's card has it:
  no row reads "<id> go?".
- Text (14 px, `--text`): the rows themselves, one line each.
- The proportional face (`--sans`) replaces monospace for human words:
  titles, reasons, the answer line. Monospace stays for machine words:
  bead ids, counts, times, stages. No face is added and none is
  downloaded: `--sans` is already a token and already renders
  `.card-title` and `.panel p`; the proposal extends it to the home
  rows and feed lines.
- A title runs one line and ends in an ellipsis (`overflow: hidden`,
  `text-overflow: ellipsis`, `white-space: nowrap`). The full title
  stands behind the tap on the bead's page, which keeps it whole. One
  line because the examples share it (Linear's one line per item,
  GitHub's short list, Vercel's one status line) and because at 390 px
  a second line per row halves the answers that fit. Faults read in
  `--held` and landings in `--landed`, as today; nothing else gains a
  colour, and the dot (`.dot`) marks state where a word would be a
  sentence.
- On the panel, flowdraw's faces and palette stand: its SVG already
  carries both colour schemes under `.flowdraw`, and stage names,
  group names and counts stay its monospace. State adds three colours
  taken from the yard's tokens, the same on the track and the home
  rows: lit green (`--running`), fault red (`--held`), person's-turn
  blue (`--accent`); quiet blocks and track stay flowdraw's platform
  grey. No fourth colour and no face are added: hollow versus solid
  and the diamond say the rest.

### 3.5. The same pages on a wide screen

The smallest version is one layout calm on both: the same HTML, the same
order, the same one-line rows at every width. The panel is the same SVG
at every width too: on a wide screen more of each diagram fits without
panning, and that is the only change. What changes with width is only
the shell, as today: under 700 px the centre stands alone with the menu
as a bottom bar; at 700 to 1099 px the menu stands left; at 1100 px and
wider the right column returns. On home at the wide width the column
shows only "The line" panel (the numbers); its "Waits for you" panel is
hidden on home, because that queue now stands at the top of the centre
and two blocks about waiting, far apart, were the complaint, and the
hiding holds on the live re-read too: home's aside re-reads a home mode
of `/line` (a query parameter, as `?all=1` is on `/beads`), which
renders only the line panel, while `/line` itself still renders the
waits and the line whole. Every other page keeps the column as it is:
this design touches no page but home, the panel rendering of `/flows`
and, on home only, the aside beside it. What must not change with width:
no wide-only block, no hover-only action, no second waits idea, no wider
feed, no full titles returning where rows truncated them. The centre
keeps its 600 px maximum so a row reads the same on a monitor and a
phone; a diagram pans inside it wherever the track runs past it.

### 3.6. What it becomes when asks exist

When yardr-jbpa and yardr-qzag have landed, the needs list shows open
asks addressed to the operator in the same rows, not in a second list:
the question in plain words first, the bead id small at the row's end,
the asker's recommendation and waits-since faint. A bead that both waits
(by hold, block, strand or manual stage) and carries an open ask stands
once, with its fault first and its question after. Footplate groups by
topic; the web stays flat and shows the bead's first `topic:` label
small where there is one, because the first screen holds a handful of
rows, not ten questions in three groups. No pick moves a bead from the
web: the view stays read-only, and the row links to the bead's page,
whose notes carry the ask once jbpa renders it there. On the panel an
open ask lights its stage's blue lamp, as a manual wait does: a question
addressed to the person is the person's turn. Until the asks land, the
list is today's waits with tomorrow's row shape, so the change lands
twice without a second design.

## 4. Left out

**A new route for the full feed.** `?all=1` on `/` does it, as `?all=1`
on `/beads` already lists the closed too. A new route would be a second
feed to keep, against the rule that a new view is a new feed in the
centre column, never a new arrangement.

**Grouping by topic on the web.** Footplate groups decisions by the
bead's first `topic:` label because ten questions want three groups.
Home holds a handful of rows; the label shows small where there is one,
and grouping returns if the list outgrows one screen.

**Dismiss, snooze, Later and Decline on the web.** Footplate puts a
question aside and declines it; the web takes no write anywhere, and a
first write route would break the read-only rule the whole of docs/web.md
stands on. The row links to the bead; the answering happens where
writing lives.

**A live overview.** Home's counts stay a snapshot and the column stays
live, as today. Making the overview live would re-read it on every event
like `/beads` does; the answer line going stale between reloads is the
cheaper fault, and the column beside it (and `/line` a tap away) is
already live. The panel is the exception that proves it: it re-reads
itself on every event, as `/line` does, because a signalman watches a
live board and a stale board misleads worse than a stale count.

**Per-depot rows.** One "Yard N" row replaces five depot blocks.
Per-depot rows would be five rows where one answers, and the panel
breaks the open beads down per depot, flow and stage, drawn.

**A new route for the panel.** `?panel=1` on `/flows` does it, as
`?all=1` on `/beads` lists the closed too. A new route would be a new
handler, a new JSON shape and a second `/flows` to keep, against the
rule that a query parameter comes before a new route.

**One depot at a time, or folded depots.** A picker showing one depot,
or a folded list opened depot by depot, would hide the yard the
signalman must see whole behind taps. Scroll hides nothing: the page
scrolls vertically across depots and each diagram pans horizontally at
full size.

**A vertical track for the phone.** Turning the track top-to-bottom on
narrow screens would be a second layout in flowdraw for one width, and
the names would still set the width. The same SVG serves both: the
phone pans it.

**Live blocks through the card stream.** `/flows` moves cards on named
stream events; teaching the panel those events would re-draw blocks one
by one. Re-reading the whole panel on every event is one `hx-get`,
as `/line` does, and a panel is small.

**Group names on quiet blocks.** Only a running block carries its
group, because only there does someone work it. Naming no one on every
block would be nineteen names on a backlog siding, and the stage's own
group already stands under its platform.

**A JSON change.** No field is added, removed or filtered in any answer:
`Home`, `Line`, `events` and the lists stay byte-identical. Clients
beside the browser (signalbox reads `/beads`, `/flows`, `/events`)
meet no second shape.

**Redesigns of the other pages.** `/flows`, `/beads`, `/sessions`,
`/crew` and a bead's page keep their shapes; home only links to them
and the panel only re-renders `/flows`. Each is a design of its own
when its turn comes.

**Notifications, a framework, a build step, a native app.** Not wanted
by the bead, and none is needed: the calm comes from fewer rows and one
line each, which templates and one stylesheet already do.

## 5. Open questions

Each is the operator's to decide; the costs read from the code above,
and the recommendation is mine. The proposal above leaves each open
where it touches it.

1. **How many needs rows before the overflow.** Option A: five rows,
   then "All waiting N" to `/line`. Costs one screenful at most and the
   sixth wait is one tap away. Option B: three rows. Costs less scroll
   on a bad day and more taps to see the fourth. Option C: every fault
   whole, then parked to five. Costs an unbounded first screen on the
   day everything faults, which is the day the screen matters most. I
   would take A: five faults fit 844 px with the rows and the feed's
   header peeking, and a fault count past five is itself the news the
   answer line tells.
2. **What the Running row names.** Option A: the count alone ("Running
   4"). Costs nothing and says the yard moves. Option B: the count and
   the oldest age ("Running 4 · oldest 3h"). Costs one faint span and
   says whether something stalls. I would take B: a builder running
   three days is the fault the needs list has not caught yet, and the
   age is already in `Running` (`started_at`), so no new read pays for
   it.
3. **What the Closed row names.** Option A: the count alone ("Closed
   today 10"). Costs the least and matches "nothing shown that is
   finished". Option B: the count and the newest title ("Closed today
   10 · last: the GitHub pack's source…"). Costs one truncated title
   and says what moved without opening the list. I would take A: the
   titles are one tap away at `/beads?all=1`, and the row's job is to
   say the day moved, not to report it.
4. **How many filtered feed lines before "All activity".** Option A:
   ten lines. Costs a short scroll to the full feed and keeps home's
   first paint small. Option B: twenty. Costs twice the scroll for
   twice the glance. Option C: every kept event in the 50 (section
   3.3's filter over the whole page). Costs an unbounded home on a busy
   day. I would take A: ten person-relevant events are a day's worth
   most days, and the full fifty stand behind one tap either way.

## 6. The beads it would become

For depot yardr, in the order they would land. Each is small enough that
a builder finishes it without asking. Each touches only the pages named;
no bead changes any JSON shape, and docs/web.md moves with the page in
each.

**1. The panel: flowdraw's track with the yard on it.** Goal:
`/flows?panel=1` draws every depot's flows as section 3.1 says: the
track as it really is, sidings and back edges, blocks for the open
beads newest first and past ten folded into "+N", sections lit green
while a session runs with the group's short name on the running block,
platforms linking to the stage on `/flows` and blocks to their beads,
trains' children standing together, "off the line: N" where beads
stand in no stage, bare tracks for empty flows, and the whole panel
re-reading itself on every stream event. Where to look: `SVG`,
`planOf`, the canvas and the `style` in `internal/flowdraw` for the
track to share; `flows()` and `FlowStage` in `internal/web/beads.go`
for the stages and beads; `card()` and `working()` for held and
running; `handleFlows` for the parameter; `line.html` for the self
re-read; the menu in `internal/web/server.go` for the "Yard" entry;
`docs/web.md` ("Pages", "Layout"). Done when: `?panel=1` shows the
yardr depot's four flows drawn with the edges `yardr flow show yardr`
prints; a running session lights its section green with its group
named; each block and platform links where section 3.1 says; an event
moves the blocks without a reload; `/flows` without the parameter and
its JSON are unchanged, asserted in tests; the seven-entry menu still
fits 390 px; docs/web.md names the panel and how to read it. Depends
on: this design's decision.

**2. The panel: every blocked kind at a glance.** Goal: the blocks,
lamps and platforms of section 3.1's vocabulary: solid red for held,
hollow red for gave up, hollow green for stalled, green with a red
ring for blocked on a prompt, blue with the first open blocker's id
for waiting on a dependency, a red diamond for back on `failed` or
`changes`, red and blue stage lamps, hollow "no route" platforms.
Where to look: bead 1's function for the marks to add; `held()` and
`blocked()` in `internal/web/waits.go` for the last-word pattern, the
`gave_up` event in `internal/dispatch/dispatch.go`,
`session_stalled` in `internal/store/events.go`, `openBlockers` in
`internal/store/graph.go`, the `advanced` outcome in
`internal/store/beads.go`, `FlowStage.Group` for the unrouted.
Done when: each kind in section 3.1 renders as specified against a
yard holding one of each (the tests build one); the dep id links to
the bead holding; lamps light exactly where a wait sits; docs/web.md
lists the marks. Depends on: bead 1 (the track it marks).

**3. Home answers first: the needs list.** Goal: the top of `/` becomes
the answer line with one row per wait, faults first, plain words first
and the bead id small at the row's end, with "All waiting N" to `/line`
past the decided count; a held row whose bead's events hold a `gave_up`
for that hold names the give-up and still counts once. Where to look:
`Home.Attention` and `Line` in `internal/web/home.go` and
`internal/web/waits.go`; `held()` for the reason to enrich;
`internal/dispatch/dispatch.go` for the `gave_up` event;
`templates/home.html` and `templates/aside.html`; `docs/web.md`
("Pages", "The right column"). Done when: `/` shows the answer line
and the rows of section 3.1 on a phone and wide alike; faults sort
before parked and the newest change first within each, as `waits()`
orders them; the overflow row appears exactly past the decided count
and links to `/line`; the JSON of `/` and `/line` is unchanged,
asserted in `internal/web` tests; docs/web.md names the answer line
and the row shape. Depends on: this design's decision, and open
question 1 for the count.

**4. Counts become rows.** Goal: "Running now" and "Closed today"
become rows and "Depots" and "The line" give way to the "Yard" row,
each linking out, shown only when non-zero. Where to look:
`templates/home.html`; `.lines` and `.depot` in
`internal/web/assets/yard.css`; `Home.Running`, `Home.Depots` and
`Home.Closed` in `internal/web/home.go` for the counts. Done when: `/`
shows "Running N" to `/sessions` (with the oldest age if open question
2 takes B), "Yard N · working M · held H" to `/flows?panel=1`, and
"Closed today N" to `/beads?all=1` (with the newest title if open
question 3 takes B); each row hides at zero; the per-depot stage pairs
and the ten closed titles leave the page but stay in the JSON;
docs/web.md names the rows and their targets. Depends on: beads 1 and
3 (the panel it links and the answer line's template it builds on).

**5. The feed, filtered, with all of it one tap away.** Goal: home's
feed keeps only section 3.3's events as one-line rows, newest first,
with "All activity" rendering the full 50 behind `?all=1`, new on home
after the `/beads` convention. Where to look: `inFeed()` and `entry()`
in `internal/web/feed.go` for what an entry is (the new filter is one
HTML-only function beside them, not in them); the new `?format=` value
in `internal/web/events.go` (`frame()`) for the home stream, with
`Page.stream` in `internal/web/server.go` for how home subscribes;
`templates/home.html` and `templates/entry.html`; the `?all=1` handling
on `/beads` for the parameter's shape. Done when: `/` shows only kept
events, at most the decided number (open question 4), each one line;
a landing stands once (its terminal `advanced` hidden) and a move back
on `failed` or `changes` stands; `/?all=1` shows the full 50 as today,
`?before=` pages it, and its stream is unfiltered; the home stream puts
only a kept event's fragment on top and passes the rest as their ids
alone, and the column still re-reads; `/` JSON keeps the full `events`
either way, asserted in tests; docs/web.md ("The feed") lists what home
keeps and where the rest stands. Depends on: bead 4 (it builds on the
rows' template).

**6. Weight: one large line, sans words, one-line titles.** Goal: the
answer line at `--large`, human words in `--sans`, machine words in
monospace, every row and feed line truncated to one line with an
ellipsis. Where to look: `internal/web/assets/yard.css` (tokens,
`.label`, `.lines`, `.entry-*`, `.wait`); the home and aside templates
for the classes. Done when: at 390 px no home row wraps, long titles end
in `…`, the answer line is the only 20 px on the screen, and titles and
reasons render in the sans stack while ids, counts, times and stages
stay monospace; no new face, colour or width token is added; the
`TestNoOtherHost` walk still passes. Depends on: beads 3 to 5 (it styles
the rows and lines they shape).

**7. Wide home stops repeating the waits.** Goal: on `/` at 1100 px and
wider the aside shows only "The line" panel, in the first render and
after every stream message; every other page keeps the column whole.
Where to look: `templates/shell.html` (the aside's `hx-get="/line"`
re-read) and `templates/aside.html`; `Page` and `respond` in
`internal/web/server.go` for how home names its aside; `docs/web.md`
("Layout"). Done when: `/` wide shows the numbers beside the centre
and no second waits list, including after an event re-reads the aside
(home's aside re-reads the home mode of `/line`); `/line`, `/flows`,
`/beads`, `/sessions` and `/crew` are unchanged; docs/web.md says the
aside on home is the line alone. Depends on: beads 3 and 6 (the centre
list it defers to).

**8. Asks stand in the needs list and light the lamps.** Goal: once
yardr-jbpa and yardr-qzag have landed, an open ask addressed to the
operator stands in the needs rows of section 3.6: the question first,
the bead small, the recommendation and waits-since faint, one row per
waiting bead; on the panel its stage carries the blue lamp. Where to
look: beads yardr-jbpa and yardr-qzag for the ask's shape and queue;
`waits()` in `internal/web/waits.go` for the rows it joins; bead 2's
lamps for the panel mark; `templates/home.html` and
`templates/aside.html`. Done when: a bead with an open ask and no other
wait stands in the list; a bead with both stands once, fault first;
`/line` shows the same rows with the asks' context; the asked stage's
lamp burns blue on the panel; the web still takes no write and the pick
happens where writing lives; docs/web.md names asks in the waits.
Depends on: bead 7, and on yardr-jbpa and yardr-qzag having landed.


