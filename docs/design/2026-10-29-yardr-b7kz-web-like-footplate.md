# The web view shares footplate's rail, ask card and chat

Design yardr-b7kz, proposed 2026-10-29. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead. No code
lands with this design: each piece of section 3 becomes its own bead
(section 6).

Terms used below. The *web view* is yardr's browser view of one yard:
HTML rendered by `internal/web` with `html/template`, htmx and one
stylesheet, served by `yardr web` on one yard's home. *footplate* is
yardr's desktop app for macOS and Linux (Rust, GPUI; the clone at
`footplate` beside this repository): a native window onto a yard over
the same socket API, which can hold several yards and switch between
them. The *rail* is the narrow strip at a window's far left that picks
what the window shows. An *ask card* (card) is one stored question
(`store.Ask`, `internal/store/asks.go`) as a person reads and answers
it: its question, context, options and answer controls. A card is
*weighty* when approving it widens what reaches outside the yard,
whose word or credential counts as a person's, or what runs inside
it; the yard weighs each card as it stores it (`Weight`,
`internal/store/weight.go`) and prints one `WEIGHTY · <class>: <line>`
line per class. A *stage card* is a card the yard itself puts on a
bead when the bead enters a templated manual stage
(`internal/store/stageasks.go`). The *chat* is the conversation with
the crew: the mail between the person and one crew member or user,
and the line they type the next mail in. A *yard* is one installation
of yardr; the *operator* is the person who runs it.

## 1. The question

mckean, mail 113396/113408 (29 Oct 2026): make the web view look and
feel more like footplate. Not parity. Share three things: the rail
and its sections (the same sections, in the same order, under the
same names and icons as footplate's rail); the ask card (header,
weight, options, answer controls — a card must read the same in
both, because after yardr-2nb1 the hosted web is where weighty cards
get approved); and the chat (the conversation with the crew, its
layout, and the line you type in). The bead pages, which footplate
has no equivalent for, the hosted sign-in pages, anything footplate
would have to change, and pixel parity are out of scope.

## 2. What exists today

Everything in this section can be opened. File references to the
web view are in this repository; references to footplate are in its
clone, read only.

### 2.1 The two left edges

**footplate.** The rail stands on the window's far left whatever
shows (`Footplate::rail` in `src/window/chrome.rs`, drawn by
`render` in `src/window.rs`). It is 64 pixels wide at every text
size (`RAIL`, `src/window/metrics.rs`), on the side ground. From
the top: the open yard's chip, a 40-pixel rounded square carrying
the yard name's first two letters on the yard's colour — a hash of
the name into the design system's rust scale, brighter shades on a
dark ground and deeper on a light one, with letters chosen to read
against it (`yard_color`, `yard_ink`, `src/theme.rs`); a click
opens the yard switcher, and its tooltip names the yard and the
key. Then the three modes, in the rail's order (`Mode::ALL`,
`src/model.rs`): Crew, Decide, Lines. Each is a 52-pixel tile: a
28-pixel railway glyph drawn on a canvas from the rail mock's
geometry (`src/window/ui.rs`) — a signal lamp for Crew, a semaphore
arm for Decide, two rails with three sleepers for Lines, the last
being the design kit's track icon — with the mode's label in
10-pixel text under the glyph, and a tooltip giving the mode's
name and its keys. The open tile stands on the open ground in the
text colour; the rust accent is kept for what waits on the person
reading, never for which mode is open. A tile whose mode waits on
the person carries a small pill at its top right: the unread mail
count on Crew and the count of beads holding open asks on Decide,
both in the accent, and on Lines muted the person's beads added
or closed since they last had Lines open (`Counts`,
`src/window/state.rs`; `timetable_news`, `src/model.rs`), each
with the count in words in its tooltip; while the yard is away
no tile carries a count. At the foot, where
feedback is offered, a Feedback tile, then the settings gear — a
drawn hub with a dashed ring — labelled Settings. On macOS the
content reaches into the title bar, so the rail begins with a band
the title bar's height and the chip stands below the traffic
lights; the settings sheet and the list beside the rail begin
under the same band.

Beside the rail stands one list column, 270 pixels at the usual
text size and never over 48 hundredths of the window, with the
status row at its head. Each mode's list has its own sections.
Chat's list is the crew under three group heads — This yard's
crew, Linked yards, People — then the yard's own row under Yard,
which holds the yard's notices, because notifications are mail the
yard wrote to the person. Decide's list opens with a Decisions
section head whose count stands at its right in words, then the
open asks grouped by topic where a topic holds more than one, then
a foldable Decided history and, for the operator, the grants row.
Lines' list opens with a Timetable section head, then the arrivals
row, one row per travelling line, the beads with no line, and a
foldable Quiet group. Section heads are one style: the title
semibold, its count quiet at the right (`section_head`,
`group_head`, `src/window/ask.rs`).

**The web view.** The left edge is a text menu, not a rail. The
menu is one ordered list of eleven pages (`menu`,
`internal/web/server.go`): Home (`/`), Talk (`/talk`), Yard
(`/flows?panel=1`), Flows (`/flows`), Beads (`/beads`), Sessions
(`/sessions`), Crew (`/crew`), Previews (`/previews`), Config
(`/config`), Pair device (`/pairing`, held back unless the view
is signed in), Line (`/line`). The shell (`templates/shell.html`)
prints the yard's name at the menu's head as a link home, then
each label as a link; there are no icons. The column is 200 pixels
(`--menu-width`), sticky while the page scrolls; labels are
11-pixel capitals with wide letter spacing, in the secondary
colour, and the current page stands in the primary colour with a
2-pixel rust bar at its left (`yard.css`). On a phone under
700 pixels the menu becomes a bottom bar showing four entries —
Home, Talk, Beads, Line — with the rest under a More sheet in the
menu's order. Beside the menu the centre column carries the page,
and on Home and Line alone a 300-pixel right column carries what
waits for the person and the line in numbers (`templates/aside.html`).

The web view has carried the design system's tokens since
yardr-829y (commit 822b3456): `yard.css` maps its whole palette
onto them — ground `#161718` (`--surface-app`), raised panel,
hairlines, primary and secondary text, the one rust accent
`#D3602F` (`--yard-500`), and the signal green `#4CC38A` held to
the running dot alone. It is dark in every case; footplate follows
the desktop or the person's choice between its light and dark
palettes (`src/theme.rs`). The design system itself
(`yardr-design/readme.md`, `tokens/`) asks for one accent, green
only for the running dot, secondary text at 4.5:1 or better,
targets of at least 32 pixels, counts as quiet words at the right
("none waiting", "2 waiting"), sentence-case labels, and Lucide
icons used sparingly — with one decided exception (footplate-zj5f,
16 Oct 2026): a first-level rail of mode icons, 64 pixels, each
with a label under its glyph and a tooltip, is allowed outside the
header. The web menu's capitalised labels and the absence of any
glyph sit outside that exception as the menu stands.

### 2.2 The ask card on both surfaces

One stored ask is read in both. The yard weighs it in the write
that stores it (`newAsk`, `internal/store/asks.go`), so its weight
rides with it to every surface with no client work; a card stored
before a change keeps the text it has.

**footplate.** Cards stand in the Decide mode's reading pane, one
pane per bead. The pane's head stands once for every ask of the
bead: the yard's dot in its colour, then `<yard> · bead <id> ·
<topic>` (`decision_head`, `card_head`, `src/window/ask.rs`). Each
card (`decision_card`, `src/window/asks.rs`) then reads, in order:

1. The weight, first, one line per class as the terminal prints
   it — `WEIGHTY · <class>: <line>` (`weight_line`,
   `src/model.rs`) — in the readable rust step (`text_accent`:
   rust 300 on dark, rust 700 on light), never in the error red.
   A routine card carries none and reads as before.
2. The question, the card's largest text (semibold), with how
   long it waits at its right (`asked <age>`); under it,
   `asked by <asker>` in small quiet text.
3. The context lines as written. On a run or setup card each line
   stands beside the asker's label in its own column, so wrapped
   words stay under the words and a copy of the words carries no
   label.
4. A disclosure, `▸ bead details (e)`, with the bead's body, notes
   and the asks it is about behind it, and beside it the line
   `<bead> · ask <n>` standing apart to select and copy — the two
   numbers `yardr bead answer <bead> <ask> <n>` takes.
5. The options, numbered, each a row that takes the pick with a
   click or a number key: a filled marker on the chosen one, an
   empty one on the rest, its consequence after the label. The
   recommended option stands preselected — the pick, else the
   recommendation, is what Enter answers — and `recommended:
   <why>` stands under its own row, the one place the card names
   the recommendation. The yard's `You decide` stands last, as
   the stored list has it.
6. A line for words, then the controls in one row: the answer
   button, which reads exactly what Enter does (`Answer: <n> ·
   <label> ↵`, `Answer ↵` where words alone are the answer, `Send
   ↵` where words go as a note and the ask stays open); Ask about
   this, which opens the conversation with the asker; Later; and
   Decline, whose tooltip says it says no to every option and the
   asker finds another way. Each key stands on its control.

Secret, run and setup cards replace the options with the yard's
own block: the yard's line of what is asked for and where the
value goes, or `in <yard>, when you approve, the yard runs this
as <user>, and nothing else:` over the command whole in a code
block, what the command does, what it changes, and the command
that takes it back with who may run it. Their one control approves:
`Approve in <yard>`, with its key. Only the bead's current card
takes the keys, the line and the buttons; the others end with
`decide this one`.

A weighty card's approval passes a gate before it goes (`approval`,
`src/window/ask.rs`): straight to the yard on a routine card;
through the system's unlock on a weighty one where the window has
one (the Mac's LocalAuthentication, Linux's polkit); where it has
none, the card takes the ask's number typed back first, as a
terminal asks it back; and where the yard takes no approval from
this window at all, the card shows the terminal command in place
of the control. What mail names an open ask carries a preview of
the same card into the conversation — `Waiting on you · <kind>
<ask> · asked <age>`, the question, and an Open card button that
opens the card in Decide; the card itself stands in one place.

**The web view.** The same stored ask is rendered three times,
once per place a person meets it, and the three copies have
drifted in wording: on the bead's page (`templates/bead.html`,
the asks section), at the top of Home under the waiting rows
(`templates/home.html`, the first ask standing whole under its
row), and in the right column on Home and Line
(`templates/aside.html`). Each copy prints, in the same broad
order: the weight lines in rust (`.weighty`); the question as a
plain paragraph, no larger than the page's text; the context
lines as indented notes; the options as a numbered list,
`1. <label> — <consequence>`; the recommendation as a note —
`Recommended: <n>. <label>: <why>` in two of the copies,
`Recommends <n>. <label>` on the bead's page; then the answer
form. The bead's page heads the card with `asked by <asker>` and
the time; Home and the column head their row with the bead's id
and stage, and the question stands where the bead's title would.
No copy names the yard, the ask's number as a line of its own, or
the bead beside the question: away from the bead's page — on
paper, on the hosted screen, by notification — the card does not
say which bead it decides, except by the row's small bead id.

The answer controls are buttons in a wrapping row: one button per
option, which answers with that pick when clicked; a words field;
Later and Decline. There is no preselection and no separate
answer step: a click on an option is the answer. A run card shows
the command and `to approve: <command>` as notes; a secret card
shows the yard's line and, where the card is not weighty, its
fields and a Give button. Every ask with options carries the
yard's `You decide` as its last button today: it records the
words and moves nothing, and so reads as agreement while deciding
nothing — the complaint yardr-17bj answers for stage cards.

The web view has no gate to put in front of a weighty card. On a
weighty mapping card it offers no pick, and on a weighty secret
card no giving; in their place it prints that the card is weighty
and this window has no gate, to approve it in footplate or in a
terminal on the yard's machine, with the command
`yardr bead answer <bead> <ask> <n>` (`templates/bead.html`,
`templates/home.html`, `templates/aside.html`; `beadGiveSecret`,
`internal/web/acts.go`, says the same of the giving path: the
yard refuses in its own words and the page shows them). After
yardr-2nb1 the hosted web is where strong cards — weighty
cards carrying trust or code — get approved.
That design has landed
(`docs/design/2026-10-29-yardr-2nb1-stronger-approval.md`, on
the depot's base since 2026-10-29)
and recommends that a strong card — one whose weight carries
trust or code — be approved on a hosted yard only in the hosted
web under a fresh sign-in made for that card (the gateway
re-authenticating the operator after the card's one-use
challenge, the approval following within minutes), on any yard
at a terminal on the yard's machine, and by a paired device's
token nowhere. The recommendation goes to mckean as an ask and
is not yet chosen, and nothing of it is built; until a chosen
gate is built, the hosted web cannot approve the cards this
section is about, and the design below is written for the web
that can.

Two things this design stands on have not landed. yardr-17bj (in
`new`) changes the stage card's header and drops `You decide`
from stage cards: when a stage ask is stored, the yard freezes a
header into it — the question gains ` — <bead id>: <title>`, and
the context gains, before the template's lines, `Bead: <id> —
<title> (branch yard/<id>)` and, where a finish note stands (a
note carrying a `test: … @ <sha>: pass` line), that note's first
line — and `stageAsk` appends `You decide` to no stage card,
while a card stored before still answers its `You decide` pick
as today. Because the header is frozen into the stored ask, every
surface — terminal, prime, web view, footplate, notification —
reads it with no client change. It is not in the tree: today a
stage card's question and context are the template's unchanged,
and its options end in `You decide`. Section 3.2 designs the
card's header on top of 17bj's version and names, at each place,
what reads differently until it lands. And yardr-2nb1's stronger
approval, though its design has landed with its recommendation
named, is neither chosen nor built; section 3.2 names that
dependency where it bites.

### 2.3 The chat on both surfaces

Both conversations are mail: the store's letters between the
person and one other party, newest last. They differ in
everything around the letters.

**footplate.** The conversation is chat mode's reading pane
(`talk_conversation`, `src/window/bead.rs`). Its head names the
other party in semibold, with `· <state>` beside it where the
party is a crew member — what the agent is doing, in the words
the yard lists it with — and, where that member has a session,
the terminal glyph at the head's right edge, which opens the
session's screen over the window. The mail reads as a column:
each letter stands full width up to the reading width, 880
pixels, centred in the pane (`READING_WIDTH`,
`src/window/metrics.rs`; `reading_column`, `src/window/panel.rs`).
Letters are grouped: a day's separator — the day centred between
two hairlines — stands where the day changes; a group's head is
the clock time and the sender's name in semibold (the person's
own name dimmed: the accent is kept for what waits on them), and
letters that follow under the same head carry no head of their
own. The body stands in from the column's left by a gutter the
clock's width sets (3.5 rems), roughly under the sender's name,
drawn with footplate's light Markdown — code, one level of
lists, headings to h3, bold and italic, the rest as written
(`src/markdown.rs`) — with any image at a fixed height, the
letter's reactions under it, and an open ask the letter names as
a preview (section 2.2). A bar of reactions floats over a letter
while the pointer stands on it. While the view stands above its
newest, a jump control floats at the mail's bottom right — a
down arrow, and in the accent how many mails arrived below. The
conversation scrolls in its pane; the window never scrolls.

The line you type in is a framed composer standing in the
reading column under the mail, centred and no wider than the
mail, its edges lining up with the column's: a box on the side
ground with a hairline border, holding any attachments above the
line with the reason a marked one cannot go, then the line
itself, then the cheatsheet's hint at its right. The line is
footplate's own text field (`src/input.rs`): Enter sends the
draft as one mail, Shift+Enter breaks the line, and the box
grows with its rows up to a part of the window and then scrolls.
Each conversation keeps its own draft — the text and its
attachments — across mode and row switches (`src/draft.rs`),
and a draft that did not go is put back whole with why. While
the yard is reconnecting the draft stays as typed under the
words `sends when the yard answers`, and nothing typed is lost.
Files reach the draft by drop anywhere on the window or by
paste, and go as blobs before the mail. The yard's notices stand
as a conversation of their own, under the Yard row of chat's
list.

**The web view.** The talk page (`templates/talk.html`,
`internal/web/talk.go`) is the web's `yardr chat` for one party
at a time, and only where the view acts as a user (`--as`, or a
gateway's verified user) for a browser holding the view's
cookie; otherwise the page says so in place of any conversation.
The centre fills the viewport's height and holds three bands.
At the top, the parties to talk to as a row of capitalised links
— the crew first, then the people — with an unread dot on a
party with unread mail, and the state of the party in
conversation, in footplate's words (`WithStatus`,
`internal/web/talk.go`), standing small beside that party's name
alone. Between them, the letters scroll: the pane runs
bottom-up (`flex-direction: column-reverse`), so the newest
letter is in view on open with the form under it. The newest
page (50 letters, oldest first inside the page) is live — every
event of the yard's stream reads it again and puts it in place —
and older pages are snapshots reached by an Older mail link
that htmx follows as it scrolls into view. Each letter stands
full width with a hairline under it: a head with the sender as a
small capitalised label and the time at its right, the text as
written in a preformatted block, any image under it at the
column's width, and a quiet `read` where the person wrote it and
it was read. There is no reading width of the mail's own: the
centre takes what the menu and the right column leave, and a
letter's text keeps a measure of about 90 characters
(`yard.css`). There are no day separators, no grouping of a
sender's letters under one head, and no reactions; a letter that
names an open ask carries nothing of it here — the ask stands
on Home, on Line and on its bead's page instead.

The form stands outside the live part, so what is typed survives
what arrives. It is a label — `Mail to <with> as <as>` — a
three-row textarea in 16-pixel monospace (smaller, and a phone
enlarges the field on focus) with the placeholder `Write to
<with>…`, and a row with the Send button at its right and, beside
it, the key that sends: `ctrl+Enter`, or `⌘+Enter` on a Mac,
filled in by `yard.js` from the platform, so without scripts the
page names no key, which is true. A plain Enter breaks the line;
Ctrl+Enter or Cmd+Enter sends, the opposite assignment to
footplate's. The form takes text alone: no files can be
attached. What was typed is kept only where a send fails — the
page returns with the words back in the form and the reason
above it — and one draft is all there is: leave the conversation
for another party and what was typed is gone. A text longer
than one mail is sent as numbered mails (`MaxMail`). The shell's
phone rules keep the same three bands between the centre and the
bottom bar.

## 3. The proposal

The smallest version that answers the question: the web view
adopts footplate's names, order and marks for the three shared
things, in the web's own mechanisms — server-rendered templates,
the one stylesheet, the one script — and nothing else changes
hands. No new mechanism is proposed anywhere below: the rail is
the shell's navigation restated, the card is the stored ask
rendered once instead of three times, and the chat is the talk
page's three bands rearranged. What each piece would look like
to a person is shown as they would meet it. Two dependencies
stand under the card (section 2.2): the proposal is written on
top of yardr-17bj's stage card and yardr-2nb1's hosted approval,
and says at each place what is true until they land.

### 3.1 The rail and its sections

The web view's left edge becomes a rail in footplate's sense,
carrying the same things in the same order under the same names:

1. At its head, the yard's chip: the yard name's first two
   letters on the yard's colour, the name itself as its words.
   The web view serves one yard, so the chip takes the person
   to the view's front page, as the yard's name at the menu's
   head does today — which page that is, Home or the Line page,
   is Q2 in section 5; it opens no switcher (what stays
   different, below).
2. Then the three modes, in the rail's order, as tiles with the
   mode's glyph above its label: **Crew**, a signal lamp,
   opening Talk (`/talk`); **Decide**, a semaphore arm, opening
   what waits for the person (Home's waiting rows and the Line
   page); **Lines**, two rails with three sleepers, opening the
   Yard panel (`/flows?panel=1`). The glyphs are footplate's own
   drawings carried into the page as inline SVG in the current
   colour — the track glyph is the design kit's track icon, and
   the lamp, semaphore and gear are drawn on the same 24-grid —
   so neither surface invents a second set of railway marks and
   the page loads no icon font for them.
3. At its foot, the gear tile labelled **Settings**, opening
   Config (`/config`). footplate's foot carries a Feedback tile
   above the gear where feedback is offered; the web view offers
   feedback nowhere and has no destination such a tile could
   open. The rail as drawn puts Settings alone at the foot, and
   whether that satisfies the bead's "same names and icons", or
   the Feedback tile and a destination must come first, is Q5
   in section 5.

A tile whose mode waits on the person carries a pill in
footplate's place and dress — the numeral on the pill, `9+`
above nine as footplate's badge caps it, the count in words in
the tile's title and aria-label (`2 wait for you`), no pill at
zero — but each number is specified here, because footplate's
three counts have no ready-made equal in the web view today
and the rail must not borrow a nearby number with a different
meaning. The numeral itself is an exception the operator is
asked to confirm (Q6 in section 5): the design system counts
in words and never as a badge (`yardr-design/readme.md`), and
footplate's rail is the one place its pills already stand
outside that rule. **Crew** counts the acting user's unread
letters in the store, in aggregate: the store's inbox read
counts every unread letter to the user whatever its sender
(`UserInbox` and its `Unread`, `internal/store/users.go`), and
the pill is that number at the page's render, not the talk
page's present mark, which is one boolean per party
(`Unread`, `internal/web/talk.go`). The web view
acknowledges no mail of its own: opening a conversation in
the web view calls `AckUserInbox` nowhere and marks nothing
read, so the count does not fall because a conversation was
opened here. It falls only when the mail is acknowledged
through a surface that can acknowledge it without hiding
another sender's mail — footplate, which holds what each
conversation has shown and acknowledges only the contiguous
seen prefix (section 2.1; `see` and `ack`,
footplate `src/model.rs`), `yardr chat`, or `yardr inbox` —
and it is exact again at the next page the shell renders.
The store holds one position for all of a user's senders
(the user's cursor, advanced by `AckUserInbox` through a
sequence), so a web acknowledgement "for this conversation"
through its newest shown letter would mark earlier unseen
mail from other senders read without showing it; the design
takes the aggregate count with no web acknowledgement over
either that false fall or a per-conversation seen state of
the web's own (what stays different, below; section 4).
**Decide** counts the beads carrying an open ask
to the person, one per bead however many asks it holds, as
footplate counts its decision beads: the line the shell
already builds holds each waiting bead's reasons, and the
count is the beads with an ask among them (`Waiting.HasAsk`,
`internal/web/waits.go`). It is not the shell's `Needs` number
and B1 must not reuse it: `Needs` counts everything that stops
the yard, held, blocked and parked beads included
(`attention`, `internal/web/home.go`), where Decide counts
only what asks the person to decide. **Lines** carries no pill
at all (what stays different, below). Crew's and Decide's
pills stand in the rust accent, as footplate's do. No pill
stands where the view does not act as a user, whose mail Crew
would count, or on an error page. The open tile stands on the
open ground in the primary colour, as footplate's does; the
accent stays for what waits.

The pages the rail does not name stand where footplate's list
stands: in a slim second column beside the rail, under the open
mode, as text rows with the modes' section heads in the one
style — the head in the section face, its count quiet at the
right in words. Crew's column holds Talk, the Crew page and
Sessions. Decide's holds Home, Line and Previews. Lines' holds
the Yard panel, Flows and Beads. Settings' holds the Config hub's
own lists: Depots, Groups, Routes, Peers, Dependencies, Limits,
Telemetry and Pair device. No page changes its address, its
handler or its centre; the columns only change how a person
reaches them. On a phone the bottom bar carries the rail in its
order — Crew, Decide, Lines, Settings — each at the phone's
48-pixel target, and the open mode's column opens above the bar
under More, as the hidden menu entries do today.

A person opening the web view then meets, from the left: a
64-pixel rail (chip, Crew, Decide, Lines, a gap, Settings), the
open mode's column of rows, and the page they know, with unread
mail counted on Crew's tile and what asks for a decision
counted on Decide's tile before they open anything.

What stays different on purpose, and why:

- **The chip switches nothing.** footplate holds several yards
  and its chip opens the switcher; the web view is started on
  one yard's home and serves that yard alone, so its chip is the
  yard's name and a way home. A hosted view likewise shows the
  one yard its session opened. Same mark, one yard: the colour
  still tells the person which yard they are deciding for, which
  is the chip's first job in both.
- **The rail is a page's navigation, not a window's modes.**
  footplate's modes swap the list and pane in place and keep
  each mode's place; the web view's tiles follow links and the
  browser's history is the person's back. The web keeps no
  per-mode scroll or selection between pages beyond what the
  page itself holds. Same order and names; the browser's
  machinery, not a window's, underneath.
- **Width is the browser's to give.** footplate's window is at
  least 624 pixels and its rail and list keep their sizes; a
  browser page can be 340 pixels wide, where rail, column and
  centre cannot stand together — there the column folds under
  More and the rail becomes the bottom bar, as the menu does
  today. The marks and order survive the folding; the side-by-side
  reading does not, and is not asked to.
- **Lines carries no pill.** footplate's muted badge counts
  the person's beads added or closed since they last had Lines
  open, against a watermark — an event sequence kept per yard
  in footplate's own settings (`timetable_looked`,
  `src/model.rs`) — that clears when Lines opens. The web view
  keeps no per-person watermark of the kind anywhere: the store
  holds none, and one kept in a browser would differ between
  the person's browsers and vanish with its storage, so two
  screens would state different numbers as the same fact. The
  tile therefore carries no number rather than a number with a
  different meaning under the same mark. A store-held
  per-person seen-sequence is a mechanism of its own; it is
  left out (section 4), not silently implied by the tile.
- **Crew's count does not fall on opening a conversation
  here.** footplate's Crew pill is its window's own count of
  mail not yet seen in that window: opening a conversation
  marks what it shows seen, and the yard is acknowledged only
  through the contiguous seen prefix, so unseen mail from
  another conversation holds the acknowledgement back and is
  never marked read unseen (`see`, `ack`, footplate
  `src/model.rs`). The store's one cursor per user
  (`AckUserInbox`, `internal/store/users.go`) gives the web
  view no per-conversation acknowledgement to make: a call
  through one conversation's newest shown letter would also
  mark earlier mail from other senders read. The web view
  therefore acknowledges nothing, and its Crew pill is the
  store's aggregate unread at render, falling only when
  footplate, `yardr chat` or `yardr inbox` acknowledges the
  mail. A per-conversation seen state in the browser, with a
  contiguous acknowledgement computed from it, would close
  the gap; it is a mechanism of its own and is left out
  (section 4), not silently implied by the tile. The talk
  page's per-party unread marks stand as they are beside it:
  they already read the same inbox's senders, and B1 does not
  change them.
- **The pills are read at render, not pushed.** footplate's
  badges move with its event stream. The web shell renders its
  counts with the page, as its `Needs` line does today, and
  only the right column answers the stream; a pill therefore
  states the count as the page was rendered and is exact again
  at the next navigation. Same numbers, the page's rhythm
  underneath. Crew's number is the store's unread at that
  render (above); Decide's is the line's ask count at that
  render.
- **Keys are the browser's.** footplate steps its modes with
  one modified key and says so on each tile. A page that seized
  those keys would fight the browser's own; the tiles carry
  their names and titles only, and the current page is named
  with `aria-current` as today.

### 3.2 The ask card

The web view renders the card once. The three copies in
`templates/bead.html`, `templates/home.html` and
`templates/aside.html` become one partial — an `ask` template
standing beside `card` in the shell's partials — which each
place includes with the card it has, so the wording can never
again drift between the bead's page, Home and the right column.
The one rendering reads as footplate's card reads, in the web's
own controls:

1. **Header.** Where the card stands away from its bead's page
   (Home, the right column, and any future notice), it opens
   with the pane head's line in the web's form: the yard's dot
   in its colour, then `<yard> · bead <id> · <topic>`, where
   `<topic>` is the bead's topic for every ask, as footplate's
   head has it: the bead's first `topic:` label without its
   prefix (`OpenAsk.Topic`, from the bead's labels by
   `topicOf`, `internal/store/decisions.go`; `Waiting.Topic`
   already carries it without the prefix for the web's rows,
   `internal/web/waits.go`), or `Everything else` where the
   bead has no such label (`display_topic`, footplate
   `src/model.rs`). An ask carries no subject of its own to
   put there: its stored form (`Ask`,
   `internal/store/asks.go`) holds its stage where the yard
   put it for one (`Stage`), never a subject, so the stage is
   not the header's topic and stands nowhere in the header;
   where a page already names the bead's stage (its row, its
   page head) that naming stands as it is. On the bead's page
   the page itself is that
   head — title, stage and bead id stand at its top — so the
   card there opens with the line footplate stands beside its
   disclosure: `<bead> · ask <n>`, the two numbers the terminal
   command takes, in mono where a person can select them. After
   yardr-17bj lands, a stage card's stored question and first
   context lines already carry the bead, its title, its branch
   and its finish line into every copy (section 2.2); the web
   prints them as written, as it prints the question today, and
   the head above is then the card's frame rather than its only
   naming. Until 17bj lands, stage cards carry the template's
   text alone and the head is what names the bead — the design
   does not wait for 17bj to give the card its bead, and holds
   after it without change.
2. **Weight.** First in the card, before the question: one line
   per class, `WEIGHTY · <class>: <line>`, in the readable rust
   step — the design tokens' `--text-accent` (rust 400 on the
   web's dark ground), not the fill rust the buttons and the
   current page use, and never the error red, exactly as
   footplate's weight stands apart from a fault. The web prints
   these lines today (`.weighty`); the change is the step of
   rust, the place — first, always, in the one rendering — and
   nothing else. A routine card carries none.
3. **Question and asker.** The question becomes the card's
   largest text, as footplate's is: the web's large face (20
   pixels) in the human face, semibold where the stylesheet
   gives it a weight, with how long it waits at its right
   (`asked <age>`, from the time every copy already carries).
   Under it, `asked by <asker>` in small quiet text on every
   copy, not the bead's page alone. The context lines follow as
   written; on a run or setup card the asker's name stands
   visibly as the lines' author — the web prints the lines as
   notes today, and the change is to name their speaker once,
   above them, in the yard's form (`<asker> says:`), not to
   rebuild footplate's two-column line, which exists so a copy
   carries no label: a page's text selects as it stands, and
   the bead's page already frames the text as the asker's ask.
4. **Options.** The numbered list stays the web's list, and
   gains footplate's two readings. The recommended option
   carries its mark in place: `recommended: <why>` under its
   own row, in the one wording, replacing `Recommended:` here
   and `Recommends` there. And the answer row below names the
   recommendation before a pick is made, as footplate's answer
   button does (see 5). `You decide` stands or falls exactly as
   the stored card has it: hand-written, run, setup and mapping
   cards keep theirs, as footplate's do; a stage card loses it
   when yardr-17bj lands and keeps it until then, with no web
   work either way — the web renders the stored options, never
   its own idea of them.
5. **Answer controls.** One row, in footplate's order: the
   answer first, then the words, then Later, then Decline.
   The web's controls stay buttons — one per option, each of
   which answers with its pick — with three changes of reading.
   The recommended option's button reads as the card's answer
   (`Answer: <n> · <label>`), the others by their labels alone,
   so what Enter would do in footplate is what the marked button
   does here with a click. Decline gains footplate's words as
   its title: saying no to every option, so the asker finds
   another way. And where the asker can be written to — a crew
   member or a user of the yard — the row carries Ask about
   this as a link to that conversation (`/talk?with=<asker>`),
   standing where footplate's button stands; where the asker
   is the yard itself or no one the person can write to, it
   stands not at all, rather than as a control that cannot go.
   The words field keeps its place and its words; on an ask with
   no options the row's answer button reads `Answer`, and on
   one whose words go as a note it reads `Send`, as footplate's
   two readings divide.
6. **The yard's blocks.** A run card reads, over the command,
   `in <yard>, when you approve, the yard runs this as <user>,
   and nothing else:` — the web's sentence today with the yard
   named, as footplate names it, because a person with two
   yards open must not guess which runs it — then the command
   whole, what the command does, what it changes, and who may
   take it back with the command that does, in place of today's
   `to approve:` note. A secret card keeps the yard's line of
   what is asked for and where the value goes in the same
   words both surfaces print. A setup card keeps its candidates
   and account field under the same head. None of this needs
   new stored data — the stored ask carries the command, its
   Does sentences and its take-back (`AskRun`,
   `internal/store/runask.go`) — but it is not carried to every
   place today: the bead's page reads those fields from the
   stored ask (`templates/bead.html`), while the Home and Line
   projection carries the command and its one-line description
   alone (`askReason`, `internal/web/waits.go`). B2 projects
   the Does lines, the take-back command and who may run it
   through that projection; the change is then the sentences,
   not new words of the yard's.
7. **The weighty gate.** The card's controls stand or are
   withheld exactly as the yard allows the answer, and the card
   says which, in words both surfaces share. On a routine card
   the controls stand. On a weighty card the web keeps today's
   withholding — no pick on a mapping card, no giving on a
   secret one, no approving on a run or setup one — with its
   line naming where the approval goes, until the gate yardr-2nb1
   recommends is chosen and built. That gate reaches only a
   *strong* card in 2nb1's sense: a card whose stored weight
   carries `trust` or `code`, one such class making the whole
   card strong (`docs/design/2026-10-29-yardr-2nb1-stronger-approval.md`,
   section 5.2). Where that build gives the hosted web its
   fresh sign-in, the same rendering enables the controls of a
   strong card under it — a card carrying trust or code, alone
   or beside other classes — with the approval button reading
   `Approve in <yard>` where the view is signed in for a yard
   of that name, and the card otherwise unchanged. A weighty
   card carrying neither class — `credential`, `outside`,
   `as-user` or `reads` alone, each of which 2nb1 leaves
   outside its gate with its fact — keeps the withholding
   under the same sign-in, until a decision and a gate of its
   own are made for its class; B3 must not enable it on the
   strength of this design, or a weighted card 2nb1 does not
   protect would be approved under its proof. The web grows
   no number-typing gate
   of its own: typing the ask's number into a browser proves the
   device is held, which is what 2nb1's threat already grants
   the thief, and footplate's typed-back number is a stand-in
   for an unlock the browser does not have, not a second proof.
   Until 2nb1's build lands this part is presentation only: the
   withheld card, its reason and its terminal command, reading
   the same in both.

A stage card met on Home then reads: the head naming its yard,
bead and topic (the bead's topic, or `Everything else`); its
weight, if the yard weighed it; the question —
after 17bj, ending ` — <bead id>: <title>` — as the card's
largest line, with `asked 3d` at its right and `asked by
yardmaster` under it; the context, after 17bj opening with the
bead, branch and finish lines; the options numbered, the
recommended carrying `recommended: <why>` beneath it and its
button reading `Answer: 1 · <label>`; the words field; Later;
Decline. The same card on its bead's page reads the same, under
the page's own head.

What stays different on purpose, and why:

- **A click answers; nothing is preselected.** footplate holds
  the pick apart from the answer — arrows or a click choose,
  Enter answers — because its keys act on the one current card
  and a stray Enter must not decide. A web form has no current
  card: every card on the page is live, the browser's focus is
  the person's own, and a button that only selected would need
  script to answer at all. The web keeps click-to-answer and
  marks the recommendation in words instead; the keyboard path
  is the browser's — Tab to the button, Enter on it — which
  needs no key of the page's.
- **No current card, no `decide this one`.** For the same
  reason: footplate's keys, line and buttons belong to one card
  at a time. The web's cards each carry their own form, as
  today; the price — several live forms on one page — is the
  web's standing shape, and the revision each form carries
  already refuses an answer a newer state has overtaken.
- **The bead's details stand one link away, not behind a
  disclosure.** footplate expands the bead inside the card
  because its pane is the bead's place and leaving it loses the
  card. On the web the bead's page is the details, complete;
  Home and the column link the card's head to it, as their rows
  do today. The card abroad carries its bead's name, branch
  and finish line (after 17bj) rather than its body.
- **The approval gate is the yard's, in the surface's dress.**
  footplate's gate is the operating system's unlock or the
  typed-back number; the hosted web's, under 2nb1, is a fresh
  sign-in at the gateway, and a self-hosted web view on the
  yard's machine has the terminal beside it. The card's words,
  weight and controls read the same; the proof asked before
  they act differs with what the surface can ask, and merging
  them would weaken one to fit the other.

### 3.3 The chat

The talk page keeps its three bands and its machinery — the
live newest page, the snapshot older pages, the form outside
the live part — and takes footplate's layout inside them:

1. **The conversation gets its head.** The parties' row stays
   where it is (it is Crew's list until the rail's column holds
   it), and the conversation itself opens with footplate's
   head: the other party's name in the section face, with
   `· <state>` beside it where the party is a crew member —
   the state the talk page already words in footplate's words
   beside that party's name in the row — and nothing at the
   head's right edge where footplate puts the session's screen:
   the web view reads no session's pane, and the head carries
   no control it cannot keep (what stays different, below).
2. **The mail takes the reading column.** Letters stand full
   width up to 880 pixels, centred in the centre column — the
   web's `--centre-width` gives way, on this page, to a
   reading width of footplate's measure — so a wide window no
   longer stretches a letter past reading and a narrow one is
   unchanged. Letters group as footplate's do: where the day
   changes, the day stands centred between two hairlines;
   a sender's run of letters stands under one head — the clock
   time and the sender's name, the person's own name dimmed —
   and a letter that follows under the same head, on the same
   day, carries no head of its own, its time standing in the
   gutter at its left where the head's clock stands. The text
   stands as written, in the human face at the page's text
   size, keeping its line breaks; an image stands under its
   letter at the column's width as today. The grouping is
   built, not found: `internal/web/talk.go` returns one flat
   slice of letters, oldest first, each with its sender and
   time, and no grouping stands anywhere today — the template's
   helpers (`internal/web/server.go`) hold none. B4 folds that
   slice into days and sender runs as it builds the page (or,
   at its smallest, adds the one template helper that does),
   and the template renders the groups; the measure is the
   stylesheet's alone.
3. **The line you type in becomes the composer.** The form
   stands in the reading column under the mail, centred and no
   wider than the mail, as a framed box: the raised panel
   ground, a hairline border, the card radius, holding the
   label `Mail to <with> as <as>` small and quiet at its top,
   the textarea without a border of its own inside it, and the
   Send row at its foot. The textarea keeps its 16-pixel text
   (a phone must not enlarge it) and its placeholder, `Write
   to <with>…`. Under the rail (section 3.1) the parties' row
   and the composer are the same page at phone width, between
   the centre and the bottom bar, as today.
4. **The keys change places.** Enter sends; Shift+Enter breaks
   the line — footplate's assignment, so a person moving
   between the two never sends half a thought with the key
   that breaks a line in the other. The hint beside Send names
   the keys that stand: `Enter` to send, `Shift+Enter` for a
   new line, filled in as today's hint is, so without scripts
   the page names no key and the form's button still sends, as
   a form's does. `yard.js` already holds the talk page's one
   key listener across htmx swaps; the change is which key it
   sends on. A pasted line break still breaks the line: only
   the Enter key's meaning moves.
5. **Each conversation keeps its draft.** What is typed and
   not sent is kept per party in the browser and put back when
   the person returns to that conversation, as footplate keeps
   a draft per conversation across its modes. The web's draft
   lives where its only script can keep it — the browser's
   session storage, keyed by the party — and is cleared when
   the mail goes. The server's keeping stands as it is: a send
   that fails returns with the words back in the form and the
   reason above it, and that answer clears nothing the browser
   kept. No draft crosses to the store: a draft is this
   browser's, as footplate's is this window's.
6. **New mail below is named.** Where the person has scrolled
   to older mail — an older page, which is a snapshot today and
   stays one — nothing is asked. On the live page the pane
   already holds the newest in view; where the person has
   scrolled up within it and the stream puts new letters in at
   the foot, a quiet pill at the mail's bottom right names
   them — `↓ <n>` in the accent where any wait — and takes the
   person to the newest, as footplate's jump control does. On
   the web the stream replaces the live block whole, so the
   count is the script counting what arrived while the pane
   stood off its foot; where no script runs, no pill stands
   and the page reads as today.

A person writing to the yardmaster then meets: the head
`yardmaster · working`; the mail in its column, grouped under
its days; and the framed line under it, which sends on Enter
and still holds yesterday's half-written mail to another crew
member when they come back to it.

What stays different on purpose, and why:

- **The text is drawn as written.** footplate draws its light
  Markdown, so a crew member's code fence and list read as
  such. The web view writes every text it shows — letters,
  notes, bead bodies — as it was written, in one rule for the
  whole view, and the talk page is not where that rule forks:
  a Markdown reading for mail alone would make the same words
  read two ways on two pages of one view. Plain text, grouped
  and measured, is the sharing here; the rendering of the words
  is not.
- **No reactions, no floating bar.** footplate's reactions ride
  on its hover and its mail reactions; the web view has no
  reaction on any page and its letters are read with a mouse,
  a finger or a keyboard alike. A tap-target bar over a letter
  is a native window's affordance; the page keeps its letters
  still.
- **No screen at the head.** footplate opens a crew session's
  pane over the window because the session runs where it can
  read it. The web view offers the session's page (`/sessions`)
  and its bead's page, and reads no pane; inventing a pane
  reading for the talk head alone would be a new surface, not
  a shared one.
- **Attachments are not the composer's yet.** footplate's draft
  carries files dropped anywhere on its window. A browser page
  takes files through its own picker, and the talk form posts
  text through a handler that sends one mail (`handleTalk`);
  carrying files is a change to that handler and its caps, not
  to the layout. The framed composer above is drawn so a file
  row can stand above the line later, as footplate's does,
  without moving the line. See section 4.
- **The yard's notices have no row here.** footplate's Yard row
  holds them inside chat's list. The web view's notices are
  Home's feed and the Line column, which the rail's Decide
  tile already counts; a second home for them inside Talk
  would split one queue across two tiles. Chat's column holds
  the parties alone.

## 4. Left out

- **The bead pages.** footplate has no equivalent for a bead's
  whole page — its bead stands in a pane and a drawer — and the
  bead below asks for none to be invented. The card on the
  bead's page changes (section 3.2); the page around it — its
  facts, edges, notes, sessions and history — does not.
- **The hosted sign-in pages.** They are the gateway's, not the
  view's; nothing here touches how a person signs in, only
  what a signed-in person's card asks before it approves
  (section 3.2, point 7, and yardr-2nb1).
- **Anything footplate would have to change.** footplate is
  read only. Where the two disagree above, the web moves; where
  the web cannot follow (its gate, its composer keys before this
  design), the difference is named as standing, not filed
  against footplate.
- **Pixel parity.** The rail is 64 pixels in both and the
  reading column 880 in both because those measures carry the
  reading, not because the surfaces must match to the pixel.
  The web's palette steps, its 14-pixel text and its phone
  folding stay its own; the design system's tokens already hold
  what the two must share.
- **A light theme for the web view.** footplate follows the
  desktop between two palettes; the web view is dark in every
  case today and the design tokens carry a light set. Giving
  the page the visitor's `prefers-color-scheme` is a palette
  change across every page, not a piece of these three, and no
  part of the rail, card or chat above needs it. Parked with
  this design, not asked in section 5 and waited on by no build
  bead below: the tokens keep the door open without walking
  through it, and a person asking for the web in light is what
  would file it.
- **A per-person Lines watermark.** The store-held
  seen-sequence that would let the Lines tile count changed
  beads as footplate's does (section 3.1): a new record, a new
  reset, and a meaning two of the person's screens could still
  disagree on. Not proposed; the tile carries no pill.
- **A per-conversation seen state for Crew, and a web
  acknowledgement.** The browser-held record of which letters
  each conversation has shown, with the contiguous-prefix
  acknowledgement footplate computes from its own
  (section 3.1): a new state, its reset when the browser's
  store goes, and a second source for a number the store
  already counts in aggregate. Not proposed; the web view
  acknowledges no mail and Crew's pill is the store's
  aggregate unread at render.
- **Files on the talk form, and mail reactions.** Each is a
  mechanism of its own (section 3.3), sized past the layout it
  would stand in. The composer is drawn to receive the file row
  later; nothing else here waits on either. B6 in section 6
  names the file work as an optional follow-up outside this
  design's requested build, filed only if the operator asks
  for it, so the two sections agree: left out here, optional
  there, required nowhere.
- **A yard switcher, a second yard, notifications as a
  conversation.** The web view serves one yard (section 3.1);
  pairing another yard to the same browser is not proposed.

## 5. Open questions

What follows is the operator's to decide. Each question stands
with its options, what each costs, and the one this design
would take; nothing below is decided anywhere else in the
document.

**Q1. The rail's form: two columns, or one widened column?**
Section 3.1 puts the rail at 64 pixels with the open mode's
rows in a second column beside it. (a) *Two columns, as drawn:*
the centre gives up 64 pixels of rail plus a slim column where
it today gives 200 of menu — on a window at the centre's
current cap the reading column narrows, and every page's shell
changes, though no page's centre does. (b) *One column of
tile-rows:* the 200-pixel menu keeps its width and gains the
chip, the glyphs beside their labels as rows, and the section
heads, in the rail's order — cheaper (the shell's grid and the
phone folding barely move) and less footplate: the rail reads
as a menu with marks, not as the window's edge, and the design
system's 64-pixel rail exception is honoured in name and order
rather than in shape. This design would take (a): the bead asks
for the rail and its sections, and (b) shares the words while
keeping the thing they named different. Either way the phone
bar is unchanged in shape.

**Q2. What Decide opens: Home, or the Line page?** Decide's
tile must land somewhere. (a) *Home (`/`):* its top already
reads as Decide's pane does — the answer, then what waits,
then the yard at work — and the shell's needs count already
points there; the cost is that Home also carries the feed,
which footplate's Decide never shows, so the tile named for
deciding opens a page that is half activity. (b) *The Line
page (`/line`):* the whole centre is what waits, parked and
late, with the line in numbers — Decide's list widened to a
page — and Home stands in Decide's column as its first row for
the person who wants the feed. The cost is a moved front door:
every existing link home, and the chip, must choose. This
design would take (b) with the chip taking the person to (b)'s
page: what waits is the yard's front door in footplate, where
the window opens on the yardmaster's conversation with what
waits counted beside it, and the feed is one row away. If (a)
is taken, section 6's first bead names Home as Decide's target
and nothing else changes.

**Q3. The strong card on the web before yardr-2nb1's build:
withhold, or a weaker gate?** Section 3.2 keeps today's
withholding until the fresh sign-in gate 2nb1 recommends —
its design landed, its pick mckean's to choose — is built.
The gate, and this question, reach only a strong card: one
whose weight carries `trust` or `code` (2nb1, section 5.2).
A weighty card with neither — `credential`, `outside`,
`as-user` or `reads` alone — is outside the question: it
stays withheld on the web under either answer, until its own
decision and gate. (a) *Keep
withholding:* a strong card on the web names its terminal
command and waits; the cost is that the hosted web — the very
surface 2nb1 makes the place of strong approvals — cannot
approve one until that build lands, and footplate or a terminal
carries them meanwhile, as today. (b) *A typed-back number in
the browser meanwhile,* as footplate offers where it has no
unlock: the cost is a gate that proves the device is held and
nothing more, standing in the place the stronger proof will
take — and, once standing, hard to be seen removing. This
design would take (a): the gap is 2nb1's build's to close, and
a weaker gate adopted for the interval teaches the exact habit
the stronger one exists to break. The question is mckean's with
2nb1's decision, not this bead's to pre-empt; it is asked here
only because this design is where the two meet.

**Q4. The composer's Enter: footplate's, or the web's standing
key?** Section 3.3 moves the web to Enter sends, Shift+Enter
breaks. (a) *Move:* one assignment in both, and a person who
learns it in one keeps it in the other; the cost is every
current web user's habit, and any hand-written instruction that
names Ctrl+Enter, wrong from the day it lands. (b) *Keep
Ctrl+Enter / Cmd+Enter:* the textarea's common web reading, at
the cost of the two surfaces teaching opposite keys for the
same line forever. This design would take (a): the line you
type in is one of the three things the bead names, and its
first key is the largest part of how it feels. The bead in
section 6 is written for (a) and is one line of `yard.js` to
reverse if (b) is taken.

**Q5. The rail's foot: the three mode tiles alone, or the
utility tiles too?** The bead asks for the rail's sections,
names and icons to match footplate's, and footplate's foot
carries a Feedback tile above Settings where feedback is
offered; the web view offers feedback nowhere and has no
destination such a tile could open. (a) *Match what the web
can open:* the chip, the three mode tiles and Settings stand
as the rail, and a Feedback tile joins the foot when — and
only when — the web view gains a feedback destination, as
footplate itself shows the tile only where it is offered. The
cost is a literal reading of the bead left unmet at the foot:
the foot is one tile shorter than footplate's where feedback
stands. (b) *The rail is incomplete without the tile:* a
feedback destination for the web (a form, where it goes, who
reads it) is built first and the tile stands from the rail's
first day. The cost is a feedback mechanism no bead has asked
for, built to satisfy an icon, standing ready to collect
words nobody has undertaken to read. This design would take
(a): footplate's own tile is conditional in the same way, so
(a) matches footplate's rule as well as its shape, and (b)
invents a service to complete a picture. Section 3.1 is drawn
for (a); if (b) is taken, B1 gains the destination as its
first piece and the tile follows it.

**Q6. The rail's pills: footplate's numerals, or the design
system's words?** Section 3.1 draws each waiting tile with a
numeric pill (`2`, `9+`), its count also in words in the
tile's title and aria-label. The design system counts in
words, quiet, at the right, and never as a badge
(`yardr-design/readme.md`: "Counts are words, quiet, at the
right … Never `(2)` or a badge"), with the one decided
exception for the rail's glyphs and labels themselves
(footplate-zj5f, section 2.1). A pill's numeral stands
outside both: footplate carries it, and this design carries
it into the web view. (a) *Extend the rail exception to its
pills, as drawn:* the tile is too narrow for a word at its
right, the numeral is footplate's mark for "this mode waits",
and the word stands beside it for a screen reader and a
pointer, so no person meets a number without its words. The
cost is a second standing exception to the words rule,
owned by the rail alone. (b) *Keep the words rule whole:* no
pills; the open mode's column head carries its count in
words ("2 waiting"), as every other count in the view does,
and a closed mode shows nothing of what waits in it. The
cost is the rail's first job weakened: what waits in another
mode is invisible until that mode is opened, where footplate
— and the bead's "look and feel" — puts it on the tile
itself. This design would take (a): the bead asks for
footplate's rail, pills included, and the exception is
already footplate's; the words rule keeps every count off
the rail. Section 3.1 and B1 are written for (a); if (b) is
taken, B1 drops the pills and puts each count in its
column's head in words, and nothing else in either changes.

## 6. The beads it would become

In the order they would land. Each is small enough for one
session; dependencies stand with each. None is filed by this
design. B1 to B5 are the requested build; B6 is an optional
follow-up outside it, labelled as such where it stands.

**B1. Give the web view footplate's rail.** The shell's left
edge becomes the rail of section 3.1: the yard's chip, the
Crew, Decide and Lines tiles with footplate's glyphs as inline
SVG, the Settings gear at its foot, the open mode's second
column of rows under its section heads, and the two pills
exactly as section 3.1 specifies them — Crew's pill the
store's aggregate unread letters for the acting user
(`UserInbox`'s `Unread` at the shell's render, capped `9+`,
none at zero or where the view acts as no user), with no
acknowledgement made by the web view on opening a
conversation, so it falls only when footplate, `yardr chat`
or `yardr inbox` acknowledges the mail; Decide's beads-with-asks
counted from the line's reasons and never from the shell's
`Needs` number; and no pill on Lines; the phone bottom bar
carries the rail in its order. *Where to look:* `internal/web/server.go` (the
`menu` and `menuFor` it replaces, and the view and counts the
shell is given: Crew's count is one inbox read for the acting
user beside the line the shell already builds),
`templates/shell.html`, `internal/web/talk.go` read only for
its per-party unread marks, which stand unchanged — B1 adds
no `AckUserInbox` call there or anywhere in the web view —
`internal/web/waits.go` read only (its `Waiting.HasAsk` is
the Decide count's test; B1 edits nothing in it, where B2
owns its projection change below) and
`internal/web/home.go` read only for the `attention` count it
must not reuse,
`internal/web/assets/yard.css` for the rail alone (the shell
grid, `.rail` tiles and pills, the second column and the
phone bar; the card's `.weighty`, `.options` and
`.need-answer` rules are B2's, and the two beads' stylesheet
changes are disjoint by selector, so neither waits on the
other),
`internal/web/assets/yard.js` only if a count needs it — it
should not. *Done when:*
every page stands under the rail with its address unchanged;
the Decide target is the one Q2 settles; Crew's pill shows
the aggregate unread across senders (two senders' mail counts
together), does not fall when a conversation is opened in the
web view and no mail is marked read by that opening — a test
with unseen mail from a second sender interleaved before the
opened conversation's newest letter proves the second
sender's mail stays unread — and falls at the next render
after an acknowledgement made elsewhere; Decide's counts a
bead once for two asks and not at all for a held bead, and
no pill stands at
zero or on Lines; the pills read in words to a screen reader,
and stand as numerals only where Q6 settles for them (for
words, the counts stand in the columns' heads instead);
the phone bar and More hold as today;
`internal/web/layout_test.go`, the talk and needs tests, and
the page tests pass
(the bead extends their count rows before adding any); and a
shot of Home and of Talk at desktop and phone width stands on
the bead. Depends on Q1, Q2, Q5 and Q6 being settled; on nothing
else. Read-only on `internal/web/waits.go` (see B2).

**B2. Render the ask card once, reading as footplate's.**
The three copies become one `ask` partial (section 3.2, points
1–6): the head (the bead's topic, or `Everything else`, for
every ask; never its stage) and ask line, the weight first
in the readable rust, the question as the card's largest
text with its age and
asker, the one `recommended:` wording, `Answer: <n> · <label>`
on the recommended button, Decline's title, Ask about this
where the asker can be written to, and the run card's sentence
naming its yard. No gate changes: withholding reads as today.
*Where to look:* `templates/bead.html`, `templates/home.html`,
`templates/aside.html`, the partial list in
`internal/web/server.go`, `internal/web/waits.go` — B2 owns
this file's change (whose
`askReason` projection gains the run card's Does lines, its
take-back command and who may run it, which the bead's page
reads from the stored ask today and Home and Line drop, and
whose `Waiting.Topic` already carries the head's topic
without its prefix; B1 reads the file and edits nothing in
it), the
bead page's ask view, and `internal/web/assets/yard.css`
for the card alone (`.weighty`, `.options`, `.need-answer`
and the `ask` partial's rules; the rail's shell and tile
rules are B1's, disjoint by selector, so neither bead waits
on the other); the bead extends the
card rows of the existing ask tests
(`internal/web/asks_test.go`, `needs_test.go`) before adding
any. *Done when:* one template renders the card in all three
places; its head names the bead's topic — a bead with no
`topic:` label heads `Everything else`, and a stage ask's
head names its bead's topic, not its stage; a routine, a
weighty and a run card read alike in
each, the run card naming its take-back on Home and in the
column as it does on its bead's page; the wording is
footplate's at every point section 3.2 names; and the gate's
behaviour is byte-for-byte today's.
Depends on nothing; written to hold before and after yardr-17bj
lands, and says so in its note. Owns `internal/web/waits.go`
against B1's read-only use of it.

**B3. Stand the strong card's controls under the hosted
gate.** Where the gate yardr-2nb1 recommends is chosen and built, and the yard
serves its proof (a hosted view's fresh sign-in), the one
rendering of B2 enables the controls of a strong card only —
a card whose stored weight carries `trust` or `code`, alone
or beside other classes (2nb1, section 5.2) — under it
and reads `Approve in <yard>`; everywhere else, and for every
weighty card carrying neither class (`credential`, `outside`,
`as-user` or `reads` alone), the withholding
of B2 stands, until that class has a decision and a gate of
its own. *Where to look:* the same `ask` partial and
answer handlers (`internal/web/acts.go`), the JWT view's verified
identity and whatever 2nb1's build leaves for a page to read,
the card's stored weight (`Weight`, `internal/store/weight.go`)
read exactly where 2nb1's test reads it,
and the store's answer path, whose refusal the page already
shows. *Done when:* a strong card (`trust` or `code`, alone
or mixed with other classes) approvable under the gate
approves from the hosted web and reads as footplate's card
does; a weighty card with neither class — tested for
`credential` alone, `outside` alone, `as-user` alone and
`reads` alone — keeps B2's line and terminal command under
the same sign-in and approves nowhere in the web; a strong
card without the gate's proof keeps B2's line and terminal
command likewise;
and no surface approves a strong card on a held device alone.
Depends on yardr-2nb1's decision and build landing; on B2.
Filed only when that build is shaped.

**B4. Give the talk page the conversation's head and reading
column.** Section 3.3, points 1–2: the head naming the party
and its state, the 880-pixel centred column, the day
separators and the grouped letter heads with their gutter.
The letters, the live page and the older snapshots move not at
all underneath. *Where to look:* `templates/talk.html`,
`internal/web/talk.go` (the flat letter slice it returns today,
folded there into days and sender runs — or the one template
helper for it in `internal/web/server.go`, whose helper map
holds none today), `internal/web/assets/yard.css` (the talk
rules and the centre's width), `internal/web/talk_test.go`
(its letter rows extended with the grouping cases). *Done
when:* a long conversation reads grouped under its days at
reading measure on a wide window and unchanged on a phone — a
day change and a sender change each start a new head, and
letters between them carry none; the newest letter still
stands in view on open; and the page without scripts reads the
same. Depends on nothing; sequenced before B5, whose files it
shares.

**B5. Make the talk form footplate's composer.** Section 3.3,
points 3–6: the framed box in the reading column, Enter sends
and Shift+Enter breaks with the hint naming them, the
per-party draft kept in the browser, and the `↓ <n>` pill
where the live pane stands off its foot. *Where to look:*
`templates/talk.html` (the form), `internal/web/assets/yard.js`
(the one key listener, and the draft and pill it now keeps),
`internal/web/assets/yard.css`, `internal/web/talk_test.go`
and the draft-on-error keeping in `internal/web/talk.go`,
which stands. *Done when:* Enter sends and Shift+Enter breaks
in the framed composer; a half-written mail to one party is
there on return and gone once sent; the pill counts only mail
that arrived off the foot; and the form without scripts still
sends by its button. Depends on B4 landed (same files, taken
in order) and on Q4 settled for (a); if Q4 settles for (b),
the key point drops out and the rest stands.

**B6. Carry a file on the talk form (optional follow-up,
outside the requested build).** The composer takes
files by the browser's picker, standing above the line as
footplate's attachments do, sent as blobs before the mail
under the yard's caps, with the reason a marked file cannot
go standing by the line. *Where to look:* `internal/web/talk.go`
(the send handler and `MaxMail`'s caps), the store's blob and
mail calls the handler would use, `templates/talk.html`,
`internal/web/assets/yard.css`, `internal/web/assets/yard.js`
only for the picker's words. *Done when:* a file picked in the
composer arrives as the letter's file in footplate and in the
web alike; one over the cap is refused in the page's words and
nothing travels; and a letter with a file reads in B4's column
as a letter does. Depends on B5. Last, optional and
separable: nothing above waits on it, and it is filed only if
the operator asks for files on the form.

B1 stands alone. B2 stands alone and B3 behind it and behind
yardr-2nb1. B4 and B5 land in that order on the talk page; B6
follows B5 only as the optional follow-up it is labelled.
A bead labelled `eyes` — B1, B2's reading, B4 and B5 — reads
the yardr design system first, as this depot's rules ask.




