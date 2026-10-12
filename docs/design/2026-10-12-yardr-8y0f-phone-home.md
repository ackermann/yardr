# The web view's home on a phone: one answer, then rows

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
earlier cleanup did not get there. So: what does the home page of `yardr
web serve` show on a phone, 390 px wide, so that a person sees at once
whether anything needs them, and finds the rest one tap away?

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
stays in view while the page scrolls.

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
needs the person, and the rest is one tap away. No note from mckean with
his own examples had arrived when this was drafted; at decide those count
for more than this set.

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

Home on a phone becomes one answer, then rows, then a short filtered feed
below the fold. The first screen says in one large line whether anything
needs the person; under it each thing that does is one row in plain words,
faults first; under those one row per count, each a link; the feed starts
below, filtered to what a person can act on or be glad to see, with the
full feed one tap away. The same HTML serves both widths: what changes on
a wide screen is only the shell around it, and the right column stops
repeating the waits on home. Nothing leaves the JSON: filtering is in the
templates, and every count still links to the page that lists it whole.

The existing mechanisms carry it: `Attention` and `Line` for the queue,
`?all=1` for the full feed, the menu pages as tap targets, the tokens
`--sans`, `--small`, `--large`, `--held`, `--landed` and the dot for
weight, and htmx's stream for liveness. What is new is only: the home
template's order and row shapes, one HTML-only feed filter with its link,
one-line ellipsis in the stylesheet, and the aside showing the line
without the waits on home.

A person types nothing new. They open `/` on the phone and read the
answer; they tap a row to open its bead or its page; they tap "All
activity" for the full feed.

### 3.1. The first screen at 390 by 844, three days

Each drawing is the centre column at 390 px wide, the page head on top
and the menu bar at the bottom, in text. `●` is a status dot, the first
`›` in a row is the row's chevron on screen, `…` a title cut with an
ellipsis, and the trailing `› /page` is an annotation of where the row
links, not on screen. Times are this machine's, as today. The Running
and Closed rows show counts alone here; open questions 2 and 3 settle
whether they carry more.

Nothing needs him, two builders run, three closed since morning. The
answer is the whole screen; the feed's header peeks at the bottom edge.

```
Home
──────────────────────────────────────
Nothing needs you.

All clear.

Running 2 ›                        › /sessions
Open 27 · working 2 ›               › /flows
Closed today 3 ›                    › /beads?all=1
──────────────────────────────────────
Just happened                       › All activity
  landed yardr-n8w1 at 04:07 …
──────────────────────────────────────
Home Flows Beads Sessions Crew Line
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
Open 29 · working 4 ›               › /flows
Closed today 5 ›                    › /beads?all=1
──────────────────────────────────────
Just happened                       › All activity
──────────────────────────────────────
Home Flows Beads Sessions Crew Line
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
Open 26 · working 3 · held 1 ›       › /flows
Closed today 10 ›                   › /beads?all=1
──────────────────────────────────────
Just happened                       › All activity
  held yardr-evd6 · Pick the port …
──────────────────────────────────────
Home Flows Beads Sessions Crew Line
```

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
- "Running now": becomes a row ("Running N", open question 2, to
  `/sessions`), because a person acts on who runs nothing and a count
  with the page one tap away reassures as well as names.
- "Depots" (per-depot flows as stage-count pairs): becomes a row ("Open
  N · working M · held H" to `/flows`), because per-stage counts are for
  planning at a desk, not for a glance, and `/flows` already lists them
  per depot and stage.
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
- "The line" (open, working, held, sessions, per stage, per depot):
  becomes the "Open N" row above plus `/line` behind it, because the
  numbers are the reassurance and the breakdowns are one tap away.
- The page head and the bottom menu bar: stay as they are, because the
  bar is already the one-tap-away the question asks for.

### 3.3. The feed: what a person wants on a phone

Kept on home, newest first, one line per event (section 3.4), only:

- `held` and `unheld`: a fault arrives and lifts, the first thing a
  glance must catch.
- `session_blocked` and the return from it: a prompt waits on a person,
  then waits no longer.
- Failures: `session_harness_error`, a session that ends badly
  (`session_<state>`), a crew that gave up: what stays stopped is news
  in a way a restart never is.
- `closed` as landed or by hand, once: the day's progress, the calm
  counterpart to the faults. A landing's `advanced` to the terminal
  stage is hidden beside it, because today one landing stands as two
  entries and a phone should see it once.
- `advanced` only into or out of a manual stage: a bead arriving where
  a person works, or leaving it, is the queue moving; every other move
  is agents doing their jobs.
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
activity" on home adds `?all=1`, which renders the full 50 as today;
`/beads/<id>` history keeps every event of the bead, none left out;
`/events?format=json` keeps every event for machines. The JSON of `/`
keeps the full `events` whatever the HTML shows: the filter is in the
template, not in `feed()` or `inFeed()`.

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

### 3.5. The same page on a wide screen

The smallest version is one layout calm on both: the same HTML, the same
order, the same one-line rows at every width. What changes with width is
only the shell, as today: under 700 px the centre stands alone with the
menu as a bottom bar; at 700 to 1099 px the menu stands left; at 1100 px
and wider the right column returns. On home at the wide width the column
shows only "The line" panel (the numbers); its "Waits for you" panel is
hidden on home, because that queue now stands at the top of the centre
and two blocks about waiting, far apart, were the complaint. Every other
page keeps the column as it is: this design touches no page but home
and, on home only, the aside beside it. What must not change with width:
no wide-only block, no hover-only action, no second waits idea, no wider
feed, no full titles returning where rows truncated them. The centre
keeps its 600 px maximum so a row reads the same on a monitor and a
phone.

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
whose notes carry the ask once jbpa renders it there. Until the asks
land, the list is today's waits with tomorrow's row shape, so the change
lands twice without a second design.

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
already live.

**Per-depot rows.** One "Open N" row replaces five depot blocks. Per-depot
rows would be five rows where one answers, and `/flows` already breaks
the open beads down per depot, flow and stage.

**A JSON change.** No field is added, removed or filtered in any answer:
`Home`, `Line`, `events` and the lists stay byte-identical. Clients
beside the browser (signalbox reads `/beads`, `/flows`, `/events`)
meet no second shape.

**Redesigns of the other pages.** `/flows`, `/beads`, `/sessions`,
`/crew` and a bead's page keep their shapes; home only links to them.
Each is a design of its own when its turn comes.

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

**1. Home answers first: the needs list.** Goal: the top of `/` becomes
the answer line with one row per wait, faults first, plain words first
and the bead id small at the row's end, with "All waiting N" to `/line`
past the decided count. Where to look: `Home.Attention` and `Line` in
`internal/web/home.go` and `internal/web/waits.go`; `templates/home.html`
and `templates/aside.html`; `docs/web.md` ("Pages", "The right column").
Done when: `/` shows the answer line and the rows of section 3.1 on a
phone and wide alike; faults sort before parked and the newest change
first within each, as `waits()` orders them; the overflow row appears
exactly past the decided count and links to `/line`; the JSON of `/`
and `/line` is unchanged, asserted in `internal/web` tests; docs/web.md
names the answer line and the row shape. Depends on: this design's
decision, and open question 1 for the count.

**2. Counts become rows.** Goal: "Running now", "Depots" and "Closed
today" become the three rows of section 3.1, each linking out, shown
only when non-zero. Where to look: `templates/home.html`;
`.lines` and `.depot` in `internal/web/assets/yard.css`;
`Home.Running`, `Home.Depots` and `Home.Closed` in
`internal/web/home.go` for the counts. Done when: `/` shows "Running N"
to `/sessions` (with the oldest age if open question 2 takes B), "Open
N · working M · held H" to `/flows`, and "Closed today N" to
`/beads?all=1` (with the newest title if open question 3 takes B); each
row hides at zero; the per-depot stage pairs and the ten closed titles
leave the page but stay in the JSON; docs/web.md names the rows and
their targets. Depends on: bead 1 (it builds on the answer line's
template).

**3. The feed, filtered, with all of it one tap away.** Goal: home's
feed keeps only section 3.3's events as one-line rows, newest first,
with "All activity" rendering the full 50 behind `?all=1`. Where to
look: `inFeed()` and `entry()` in `internal/web/feed.go` for what an
entry is (the new filter is HTML-only beside them, not in them);
`templates/home.html` and `templates/entry.html`; the `?all=1` handling
on `/beads` for the parameter's shape. Done when: `/` shows only kept
events, at most the decided number (open question 4), each one line;
a landing stands once (its terminal `advanced` hidden); `/?all=1` shows
the full 50 as today and `?before=` pages it; the stream still puts
each event's fragment on top and the column still re-reads; `/` JSON
keeps the full `events` either way, asserted in tests; docs/web.md
("The feed") lists what home keeps and where the rest stands. Depends
on: bead 2 (it builds on the rows' template).

**4. Weight: one large line, sans words, one-line titles.** Goal: the
answer line at `--large`, human words in `--sans`, machine words in
monospace, every row and feed line truncated to one line with an
ellipsis. Where to look: `internal/web/assets/yard.css` (tokens,
`.label`, `.lines`, `.entry-*`, `.wait`); the home and aside templates
for the classes. Done when: at 390 px no home row wraps, long titles end
in `…`, the answer line is the only 20 px on the screen, and titles and
reasons render in the sans stack while ids, counts, times and stages
stay monospace; no new face, colour or width token is added; the
`TestNoOtherHost` walk still passes. Depends on: beads 1 to 3 (it styles
the rows and lines they shape).

**5. Wide home stops repeating the waits.** Goal: on `/` at 1100 px and
wider the aside shows only "The line" panel; every other page keeps the
column whole. Where to look: `templates/shell.html` and
`templates/aside.html`; `Page` and `respond` in `internal/web/server.go`
for how home names its aside; `docs/web.md` ("Layout"). Done when: `/`
wide shows the numbers beside the centre and no second waits list;
`/line`, `/flows`, `/beads`, `/sessions` and `/crew` are unchanged;
docs/web.md says the aside on home is the line alone. Depends on: beads
1 and 4 (the centre list it defers to).

**6. Asks stand in the needs list.** Goal: once yardr-jbpa and yardr-qzag
have landed, an open ask addressed to the operator stands in the needs
rows of section 3.6: the question first, the bead small, the
recommendation and waits-since faint, one row per waiting bead. Where to
look: beads yardr-jbpa and yardr-qzag for the ask's shape and queue;
`waits()` in `internal/web/waits.go` for the rows it joins;
`templates/home.html` and `templates/aside.html`. Done when: a bead
with an open ask and no other wait stands in the list; a bead with both
stands once, fault first; `/line` shows the same rows with the asks'
context; the web still takes no write and the pick happens where
writing lives; docs/web.md names asks in the waits. Depends on: bead 5,
and on yardr-jbpa and yardr-qzag having landed.


