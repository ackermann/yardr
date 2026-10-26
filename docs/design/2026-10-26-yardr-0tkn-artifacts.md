# The yard hosts its own artifacts: pages and files agents make for people

Design yardr-0tkn, proposed 2026-10-26. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead.

## 1. The question

mckean, 26 Oct 2026: "if you run out of work think about how the yard could
host and handle its own artifacts." An artifact here is a page or file an
agent makes for people to read or use (a report, a release note draft, a
dashboard, a design mock, a screenshot set), as Claude's claude.ai artifacts
are today. Today the yard's agents put these in a scratchpad, mail a path, or
publish to claude.ai, outside the yard. The design question is how the yard
holds, versions, serves and shares them itself: what an artifact is in the
store, who may see it, how it is served, how an agent makes and updates one,
how feedback returns to the agent, what the yard's desktop and phone client
shows, and what size limits and retention apply.

Terms used below. The *originator* is the user a bead is for
(`Bead.Originator` in `internal/store/beads.go`; empty reads as the
operator's). A *version* of an artifact is one published set of files; the
artifact keeps one stable id across its versions. *Footplate* is the yard's
native desktop client (depot `footplate`); the *web view* is the browser UI in `internal/web`, and the *edge* is
the hosted relay at my.yardr.dev that reaches a yard from a phone or another
machine over the yard's one outbound link (`internal/edgelink`). A *device*
is a program of a user that calls the yard from outside it, let in by a token
the yard gave it; footplate is one.

The yardmaster's direction for this draft, after the operator chose it, is
the shape of the whole proposal: an artifact is readable inside the yard
exactly as any bead, note or blob is today, and visibility governs only
where the yard meets the outside — other users' devices and the web view.
Per-agent privacy inside the yard is left
out for all beads, not built for artifacts alone (section 4). The operator
has since answered the questions this design carried (section 5):
versions are capped, closed artifacts are kept until erased, sharing with
the yard's users is anyone's in the yard, running a
page is an act on the yard's machine (the Mac here) and never happens over
the edge, and a page is one file dressed by yard-shared style and script
it may override inline. One later answer settles who may erase an
artifact (section 5). The first version is local first — the yard's
machine and the desktop client on that machine, with nothing served
through the edge at all — and edge pages and links for outsiders are
out of it altogether, named in section 4 as their own following work
with the operator's answers for them already settled there. What
follows builds those answers, not the options they replaced.

## 2. What exists today

**Files are blobs, and they already carry pages.** `internal/store/blobs.go`:
a blob is any bytes under a cap, kept in the yard's home under `blobs/`
(`BlobsDir`, so `~/.yardr/blobs/<hash>`) in a file named by the SHA-256 of
its bytes, written once and never changed. `PutBlob` on the store API
(`internal/store/api.go`) puts one and answers a `BlobRef` (hash, type read
from the bytes by `http.DetectContentType`, byte count, and the sender's file
name kept on the letter that carries it, never beside the blob). `BlobRef.Kind`
already tells the three kinds this design needs apart: an *image* (a PNG or
a JPEG), a *page* (`text/html; charset=utf-8`), and a *file* (everything
else). One blob is at most `blob_max_bytes` (8 MiB unless set:
`BlobMaxSetting`, `DefaultBlobMax`), and one note or mail carries at most
`MaxNoteFiles`/`MaxMailFiles` (64) files. `pruneBlobs` in the same file
removes a blob that no mail event, no note and no kept hook delivery names,
after `BlobGrace` (one hour, so a file put just before the note that names it
survives); a blob a note names is kept for as long as the note is.

**Beads and notes are the yard's versioned record, open inside the yard.**
`store.Bead` (`internal/store/beads.go`) has an id that never changes, a
title, a body, a type, an originator, labels and a free-form metadata map.
`store.Note` (`internal/store/graph.go`) is one dated, ordered (`Seq`) entry
on a bead, written by `AddNote`/`AddNotedAs`, and a plain note may carry
files as `[]BlobRef`. The command surface exists: `yardr bead note <id>
--file <path>` (`internal/cli/bead_note.go`) puts each file with `PutBlob`
and attaches it, and wherever the note is read the files stand under its
words. Bead types are free-form names held only to `checkName`; two types
carry knowledge instead of work, `journal` and `memory` (`NotWork`,
`TypeJournal`/`TypeMemory` in `internal/store/beads.go`), and for those
`resolveFlow` (`internal/store/flows.go`) returns no flow at all, so nothing
routes them and they are never ready. A crew member's journal is exactly
that shape in production: a bead of type `journal` in stage `journal`,
created directly by the store (`internal/store/crew.go`), whose whole
history is its notes. Inside the yard, reads are open: `ListBeads`, `Bead`,
`GetBead`, `Notes` and the event stream answer any caller that can reach
the store, and a note's blob answers any holder of its hash (`NoteBlob` in
`internal/store/blobs.go`). This design keeps that openness for artifacts
(section 3.2); the stricter rules of mail (`MailBlob` answers only the
sender's or recipient's viewer) show the gated shape a lookup takes where
one is wanted.

**A note's page already runs, sandboxed, in the web view.** The view
(`internal/web/server.go`, `routes`) serves `GET /blobs/{hash}` and
`GET /blobs/{hash}/{name}` on its own origin (default `127.0.0.1:8791`,
`DefaultWebAddr`). `handleBlob` (`internal/web/beads.go`) serves a blob some
note names (`Store.NoteBlob`), replacing the view's content security policy
with the inert `pagePolicy` for a page: scripts off, `default-src 'none'`,
only `data:` images and inline style allowed. A second listener, the
*sandbox origin* (default `127.0.0.1:8789`, `DefaultWebPagesAddr`), serves
the same note blobs under `scriptedPolicy` through `handlePagesBlob`:
inline scripts may run, but fetches, frames, workers, forms and popups are
refused and the `sandbox` directive without `allow-same-origin` gives the
page an opaque origin, so it holds none of the yard's cookies and cannot
call the yard's API. A bead's page already links a note's page both ways:
open inert on the view's origin, and "run" on the sandbox origin. Images
serve inline; every other file downloads (`serveBlob` sets
`Content-Disposition: attachment` by kind). Two limits of this machinery
matter below. On a plain loopback yard the sandbox origin checks no
identity, so whoever holds a page's hash can fetch it there. And a page
that runs is not contained to the yard: run opens the page top-level, and
the policy's own account (`scriptedPolicy` in `internal/web/beads.go`, and
`docs/web.md`) says such a page can navigate itself away carrying its own
words, and anything a reader typed into it, in the address it navigates
to, and that no released browser blocks its WebRTC connections, though
fetches, workers, frames, popups, cookies and storage are blocked.

**Nothing erases a bead, nothing lists beads by type in public, and no
outsider link exists.** `Prune` (`internal/store/prune.go`) removes old
events, ended sessions, webhook bodies and blobs no remaining letter
names; its own account says a bead and its notes stay, being state, not
log. The store API (`internal/store/api.go`) has no bead deletion, and
`yardr bead --help` offers none: closing a bead is the strongest removal
that exists. Notes have no delete operation either. The public socket
surface a program that is not yardr may rely on (`Calls` in
`internal/rpc/api.go`, promised by `docs/api.md`) offers `Bead` for one
bead and `Arrivals` for the travelling work board, and no bead listing by
type; `Arrivals` (`internal/store/arrivals.go`) is built from the default
bead listing, which leaves knowledge types (`NotWork`) out, so an artifact
could never appear on it. Footplate's client (`src/api.rs` in depot
`footplate`) reads only the calls and fields that page lists.

**Devices and the edge already serve blobs to named people.**
`GET /user/blobs/<hash>` (`userBlob` in `internal/rpc/user.go`) serves a
device, let in by its user's token, a note's or hook delivery's blob, a
mail's blob only to its sender's or recipient's device, and, for one hour,
a blob that same user's device put and no letter names yet (`unclaimedPut`,
keyed by `BlobPutBy` in `internal/store/blobs.go`). The same mux is what
the edge link carries: a yard with the `edge` setting keeps one signed
WebSocket to the edge open (`internal/edgelink/link.go`), and a device
paired through the edge reaches the yard's `/user/` calls down it, its
address shown by `linkAddr` in `internal/cli/devicepair.go`. The device
listener already binds a call to the device's own user and refuses any
other (`asUser` and `selfArg` in `internal/rpc/user.go`). Nothing today
mints a link for an outsider: pairing codes pair a yard or a device and
expire (`Expires` in `internal/store/pairing.go`), and a device token
belongs to a user. Who may do what is asked through the store's policy
gate (`may` and `coreRules` in `internal/store/policy.go`, one `Action`
per gate), and a user is an operator or a member (`RoleOperator`,
`User.IsOperator` in `internal/store/users.go`).

**Feedback and the native client need no new channel, but the device
note path does not wake anyone today.** A note written to a bead with a
live session names that session in `Note.Notify`, and the daemon tells
the session's agent once, without interrupting a turn; that naming is
done only by `AddNotedAs` (`internal/store/graph.go`), which the command
line's `yardr bead note` uses and the device listener's plain `AddNote`
call (`Calls` in `internal/rpc/api.go`) does not, so a comment written
from a device today stands silently until somebody reads the bead. Mail
reaches a user's inbox on every client. Footplate already speaks blobs:
`BlobRef`, `put_blob` and `page_url` in its `src/api.rs` (the browser URL
for a page is the view's `/blobs/<hash>` route under a configured base
address), its window draws a note's or mail's image inline and shows a
page as a row the reader opens in the system browser, and it writes
notes with the same `AddNote` call. It has no embedded web engine, and
this design adds none.

**Two boundary paths answer a device today without asking whose artifact
it is.** The device listener's bead call (`UserServer.bead` in
`internal/rpc/user.go`) returns `Store.Bead` as it stands, and its event
stream (`deviceEvent` in the same file, behind `/user/events`) holds an
event back only where the store's `ActSee` rule refuses the device's
user — a rule that today refuses nobody. Both are boundary surfaces, and
section 3.2 puts the artifact rule on them where they already bind every
call to the device's own user (`asUser`, `selfArg`). The local socket is
a different surface: inside the yard, it stays as open as the store.

**The web view's generic bead page and event stream answer the same
way.** `GET /beads/<id>` (`handleBead` and `beadPage` in
`internal/web/beads.go`) reads the bead with `GetBead`, its notes with
`Notes`, checks each note file with `NoteBlob` and adds `History`, with
the acting user (`acting`) consulted only for what moves the page
offers, never for whether the bead may be shown. `GET /events`
(`handleEvents` in `internal/web/events.go`) follows the store and sends
every event's frame to whoever holds the stream. This web handler, not
the local socket, is the one the edge carries (`KeepEdge` in
`internal/cli/web.go` serves the view's handler; `internal/server/edge.go`
carries that handler over the link), so both routes reach a phone or
another machine. Section 3.2 therefore puts the artifact rule on them
too; the local socket's bead reads and event stream are a different
surface and stay open inside the yard.

**An agent already has one approved way to ask a person to run a
command.** A run ask (`Ask.Run` in `internal/store/asks.go`, the command
list `runList` in `internal/store/runask.go`, written with `yardr bead
ask --run`) proposes one yardr command; a person approves it, and the
yard runs exactly that command as the approver, recording what it
printed. A card whose approval widens what reaches outside the yard is
*weighty* and stands behind its own gate (the ask's account in
`internal/cli/bead_ask.go`). An outsider link is exactly such a widening,
and section 3.2 routes an agent's link through this card rather than
inventing an approval of its own.

**Home's feed is built from the same events, with no boundary filter
either.** `GET /` (`handleHome` in `internal/web/home.go`) reads its
feed from `Server.feed` (`internal/web/feed.go`), which walks
`EventsSince` backwards and turns each event it keeps (`inFeed`) into an
`Entry` whose text carries a created bead's title and a noted event's
words (`reader.entry`, `reader.noteOf` in the same file); `keptHome`
only narrows that page to what stands on a phone (`inHomeFeed`), and
the same feed is answered as the page's JSON and, with `?all=1`, whole.
Nothing in that path asks whose artifact an event names, so a private
artifact's creation and publication would stand on home for any viewer
who can open it. Section 3.2 puts the same filter there.

**Any bead may be edged to any other today.** `AddDep` and its one
write path `w.addDep` (`internal/store/graph.go`; bead creation and
folding write through the same path) load the two beads and refuse a
self-edge, a folded edge and a cycle, but never look at either bead's
type, and a bead's page shows its blockers, its dependencies and its
discovery cards (`internal/web/beads.go`). An edge naming an artifact
would therefore put a private artifact's id and title on a visible
bead's page and in that bead's `dep_added` event. Section 3.2 refuses
such edges outright instead of filtering every place they surface.

**The edge does not yet carry a connected yard's web view, and has no
tokenless route.** In the hosted router (`internal/router/router.go` in
the `yardr-hosted` depot) a yard reached by its own outbound link
(backend `connected`) is forwarded its device calls, but a web request
is forwarded only for a hosted yard (backend `vps`); the router's own
answer for the rest is that the edge does not carry its web view yet.
A request with no account session is refused there but for the landing
page, and no `/shared/<token>` route exists; `serveWebLinked` is the
session-bound web path. The device listener's calls — footplate's
whole surface — are carried for connected yards today; the web view
over the edge, and an outsider following a link to a connected yard,
are not. Section 3.3 therefore drafts the first version local-first,
and section 4 names the router work it waits for.

**The sandbox origin is not carried by the edge, and admits no acting
user.** `yardr web` starts the view and the sandbox origin as two
listeners (`internal/cli/web.go`), and the edge link carries only the
view's handler; the sandbox origin checks no identity but the gateway's
token in JWT mode (`PagesHandler` in `internal/web/server.go`), never the
view's `--as` cookie. A run address is therefore an address on the yard's
machine (the Mac here) holding a blob hash, as a note's run address is
today — which is why section 3.3 makes running an act on that machine
alone. Its blob read is `handlePagesBlob`
(`internal/web/beads.go`), which asks only `NoteBlob` and names no
viewer: once `NoteBlob` learns to skip blobs only an artifact's notes
name (section 3.2), a private artifact's page would not be found there
at all, so the run needs a read of its own on that listener alone
(section 3.3).

**Linked shared style or script cannot load on the run origin.**
`PagesHandler` (`internal/web/server.go`) sets
`Cross-Origin-Resource-Policy: same-origin` on every response, and
`scriptedPolicy` (`internal/web/beads.go`) runs the page in a sandboxed
opaque origin (`sandbox allow-scripts`, without `allow-same-origin`).
A stylesheet or script linked from that page is therefore not
same-origin with its response address, and the browser blocks the load.
The shared dress of section 3.3 is not linked for this reason: it is
injected inline at serve time, which needs no change to that header or
to the sandbox.

**What is missing.** There is no artifact: no stable record whose
versions are publications rather than correspondence, no list of what an
agent has published, no reading rule at the outside boundary stricter than
"any holder of a note blob's hash", no way to erase a published record,
no public filtered list a paired client may call, no outsider link, and
no command shorter than hand-attaching files to a note on a bead made for
some other purpose.

## 3. The proposal

The smallest version is: an artifact is a bead, its versions are its
notes, and only the command, the outside-boundary reading rule, the
lists and one erasure are new. No new table holds an
artifact or a version, no new file store is built, and no new serving
origin is opened.

### 3.1 What an artifact is in the store

A new bead type, `artifact` (`TypeArtifact` beside `TypeJournal` in
`internal/store/beads.go`), entered in `NotWork` so `resolveFlow` returns
no flow for it, nothing routes it, and it is never ready — the journal's
shape, taken whole. Its one stage is named `artifact`, as the journal's
is `journal`. The bead's title is the artifact's title, its originator is
who it is for (set at creation as every bead's is, `NewBead.For` and
`originatorFor`), and its body says what the artifact is.

A *version* is one plain note on that bead carrying at least one file.
The note's words are the version's note ("first draft", "with the
September figures"); its files are the version, attached exactly as
`yardr bead note --file` attaches them today, each put once by `PutBlob`.
Version numbers are derived, never stored: the file-carrying notes of
the bead, in `Seq` order, are versions 1, 2, 3, and the last of them is
current. A note carrying no file is a comment (section 3.5), never a
version, so discussion cannot mint a version by accident. Each version
note also records the bead that published it, in one new field on the
note record, `Note.Ref` — the handle of the bead the note points at.
On a version note it names the publishing bead (empty when the version
was published from a terminal with no bead behind it); on the pointer
notes of sections 3.4 and 3.5, which stand on a publishing bead, it
names the artifact instead. One field, read the same way everywhere:
the bead this note is about, held in the record and never in words a
page renders. The artifact's
id is the bead's id and never changes; a version is named by the
artifact's id and its number. Because versions are notes, `pruneBlobs`
already keeps their blobs for as long as the notes stand, history
between versions is the bead's existing note list, and closing the
artifact bead closes the record without deleting a single byte by
itself.

What is new in the store is deliberately small: the type constant and
its `NotWork` entry, one gated blob lookup for the outside surfaces
and one pages-only lookup for the local run (sections 3.2 and 3.3),
the dedicated share write and its guard on the generic
paths (section 3.2), the `artifact_from` backlink, the `Note.Ref`
record on version and pointer notes and the comment pointer that
wakes the bead the latest version records (sections 3.1, 3.4 and
3.5), the refusal of any edge naming an artifact (section 3.2), one
filtered public list (section 3.6), and an artifact-only erasure
(section 3.7).
Everything else — creation, notes, files, listing beads by type — is a
call the store already has (`CreateBead`, `AddNote`, `PutBlob`,
`ListBeads`), so publishing needs no new socket method: the command
line, which is yardr itself, uses those calls as it does today.

### 3.2 Who may see it

Inside the yard, an artifact is readable exactly as any bead is today:
its record, its notes, its events and its files answer any agent or
process that can call the store, through `ListBeads`, `Bead`, `GetBead`,
`Notes`, the socket event stream and `NoteBlob` alike, with no new gate,
no viewer threaded through those reads, and no filtering of the stream.
An agent that made a report can be read back by the next agent without
ceremony, which is the point of publishing inside the yard at all.

Visibility governs only where the yard meets the outside: a device
(footplate) and the web view, which act for a named user. At that boundary an artifact is private
to its originator by default. A device or view acting for a user may
read an artifact — list it, open it, take its files — only under one of
these legs: the user is the artifact's originator (its `Originator`, an
empty one reading as the operator the yard's setting names, as
elsewhere); the user is an operator; or the artifact is shared with the
yard's users (below) and the user is a named user of the yard. A view
with no acting user — a plain loopback view with no `--as` and no
gateway token — satisfies no leg and is served no artifact, not even a
shared one, exactly as such a viewer is served no mail file today.
Nobody outside those legs is listed the artifact or served its files
at the boundary, and an unknown or forbidden id answers there as a
missing one. This promise is about the boundary surfaces only; it does
not claim an agent inside the yard cannot read the artifact, because
today it can read every bead.

Sharing with the yard's users is one explicit act, and the operator has
settled whose act it is: anyone in the yard may share or unshare, and
nobody shares by merely setting a label. The act is recorded as the
label `artifact:shared` on the bead — visible on every list the bead
appears in, needing no migration, and unable to steer work because an
artifact is never routed — but the label is written only by the one
store write behind `yardr artifact share` and `unshare` (section 3.4),
which records who shared in its event as every store write does. The
generic paths refuse it: `CreateBead` and `UpdateBead` refuse the
`artifact:shared` label on an artifact bead, in the shape of the field
guards (`checkFlowChange`, `checkMergeOK` in `internal/store/beads.go`)
that already keep some fields from generic patches, so no bead update,
however sent, widens an artifact's outside visibility as a side effect.
This guard is not per-agent privacy: it does not hide the artifact from
any agent, it keeps one boundary act deliberate and attributable — the
operator's words were that nobody shares by merely setting a label.

The boundary rule is enforced where the boundary is, with a viewer the
surface already trusts. The device listener fills the viewer with the
device's own user and refuses any other (`asUser` and `selfArg` in
`internal/rpc/user.go`); the web view names who it acts as. One new
store lookup, modelled on `MailBlob`, carries the rule for files:
`ArtifactBlob(ctx, viewer, hash)` answers the blob only when some note
on an artifact bead names it and the viewer holds a leg above. The web
view's blob route and the device blob route (`userBlob`) try
`ArtifactBlob` for their viewer, and `NoteBlob` learns to skip blobs
that only an artifact's notes name, so an artifact file is never
served at the boundary under the anyone-with-the-hash rule a working
bead's screenshots live under; a hash named by both an ordinary note
and an artifact keeps the ordinary note's rule for those bytes, since
they were published there. The one exception is not a boundary route
at all: the sandbox listener on the yard's own machine reads artifact
pages through its own lookup (section 3.3), so a private artifact can
run where it was made without restoring hash-only access on the view
or on any device. The artifact list and viewer pages
(section 3.3) and the public list call (section 3.6) ask the same legs
for their acting or device user before showing a row. Two device paths
that exist today take the same check, and this is a requirement, not a
choice: the listener's bead call (`UserServer.bead`) answers an
artifact bead only to a device user holding a leg above, and as a
missing bead otherwise, so a paired user who knows an artifact's id
reads nothing of its title, body, comments or files through the public
`Bead` call; and the device event stream (`deviceEvent`, behind
`/user/events`) drops every event naming an artifact bead for a device
user holding no leg, so the stream does not tell other devices that a
private artifact exists or when it changed. The check sits in the
listener beside the ones it already makes (its held-word cuts, its
`ActSee` drop), not in the store's policy, whose inside-the-yard
openness stands. The web view's two existing routes take the same
check for the same reason, and this too is a requirement, not a
choice: `GET /beads/<id>` (`beadPage` in `internal/web/beads.go`)
answers an artifact bead only to the view's acting user holding a leg
above, and as a missing page otherwise, the check made once before its
`GetBead`, `Notes`, `NoteBlob` and `History` reads so none of them can
serve a private artifact to another named user; and the web view's
`GET /events` (`handleEvents` in `internal/web/events.go`) drops every
event naming an artifact bead for an acting user holding no leg, before
the frame is sent, so the stream does not tell another
user that a private artifact exists or when it changed. On both
boundary streams the same filters also classify two events that name
something else: every pointer note's `noted` event — a publish
pointer's (section 3.4) and a comment pointer's alike — names its
publishing bead, so it is dropped for a user holding no leg to the
artifact its `Note.Ref` names (sections 3.4 and 3.5); and an erased artifact's
retained events name an id whose bead row is gone, so they are
dropped for every user while its erasure tombstone stands
(section 3.7). A view with no
acting user holds no leg on either route, as on the artifact pages.
Home's feed takes the same filter, and this too is a requirement, not
a choice: `Server.feed` drops every event naming an artifact bead, a
pointer note of either kind (by its note's `Note.Ref`) or a tombstoned id for the
request's acting user before any `Entry` is built from it, so neither
the event's title nor its note's words ever enter the feed; the
filter covers the first page and every paged page (`?before`), the
HTML, the JSON and the `?all=1` answer alike, because all of them are
built from that one feed, and `keptHome`'s narrower phone selection
inherits it. No read inside the yard asks any of this: the local socket's `Bead`,
its own event stream and `NoteBlob` answer as they do today — the web
stream filtered here is the view's, not the socket's.

No dependency edge may name an artifact at either end. The store's one
edge write (`w.addDep`, through which `AddDep`, bead creation's
discovered-from record and folding all write) refuses, naming the
artifact, when the bead at either end is of type `artifact`, of
whatever kind the edge would be. An artifact is related to its maker
by the `artifact_from` field and the `Note.Ref` records of
section 3.1, never by an edge, so no bead's page, blocker list,
discovery cards or `dep_added` event can carry a private artifact's
id or title to a reader of some other bead, and no boundary filter
has to chase those references. Inside the yard the refusal is the
only trace: an agent that tries to edge work to an artifact is told
an artifact takes no edges, as it is told a bead cannot depend on
itself.

An outsider — a person with no user on the yard — is not reached in
the first version at all. A link for an outsider needs a tokenless
route through the hosted router, which does not exist (section 2),
and the operator has put both that route and edge pages for connected
yards after the first version. No link record, link command or share
handler is built here; section 4 names the following work and holds
the operator's settled answers for it (how a link is minted, how long
it lasts, who may revoke one), so that work starts decided rather
than designed again. Sharing in this version is the device and view
sharing above, and nothing else leaves the yard.

### 3.3 How it is served

The web view gains two pages and no new origin. `/artifacts` lists the
artifacts the acting user may read at the boundary (section 3.2) —
title, originator, current version number, when it was published, and
who published it — newest first. `/artifacts/<id>` is the viewer: the
current version's files, each image inline, each page openable inert on
the view's origin under the artifact page policy below, each other file as a download;
below them the version list, any version openable the same way, and
the bead's comments. Both pages are ordinary view pages (a handler in
`internal/web`, a route in `server.go`'s `routes()`, a template), they
answer JSON to whoever asks as every view page does, and every read
behind them names the view's acting user as its viewer. With no acting
user, no artifact is listed or served.

Every page the yard serves gets the yard's shared dress first. One
stylesheet and one script, held with the web view's own assets
(`internal/web/assets`, their bytes changing only with the yardr
build), carry the skeleton the operator pointed at: colour scheme and
light and dark tokens, safe-area padding, the body font and colours,
image limits, the `[hidden]` rule. They are not served at an address
and not linked from the page: on the run origin a linked stylesheet or
script cannot load at all (section 2: `PagesHandler` answers
`Cross-Origin-Resource-Policy: same-origin` while the page runs in an
opaque sandboxed origin). When an artifact page is served — inert or
run — the serving handler instead injects the shared stylesheet as an
inline `<style>` and the shared script as an inline `<script>` into
the page's head, ahead of the page's own inline style and script,
creating the head where a fragment has none. The page then defines
its tokens on `:root`, styles its own elements and overrides any
helper of the shared script, and its inline words win because they
come after the shared ones. No policy changes for this: `pagePolicy`
and `scriptedPolicy` already allow inline style, `scriptedPolicy`
already allows inline script, and the inert policy's `script-src
'none'` simply leaves the injected shared script unrun on an inert
page, as it leaves the page's own. No header changes either — the
sandbox and the `Cross-Origin-Resource-Policy: same-origin` answer
stand — no other host is allowed, no page loads a font, a stylesheet
or a script from anywhere else, and generic note pages keep today's
policies and no injection. Because the dress travels inside the page
itself, no asset route is needed to show a dressed page. A
published page is still one self-contained file of its own, carrying
its images as `data:` and its words inline; what it may not carry in
this version is a file beside it (section 4). A version may carry
further standalone files — a screenshot set is several images in one
version — and each is served and shown on its own; a page naming a
sibling file by relative path is not served as a site.

A page runs — executes its shared and inline script on the sandbox
origin, as a note's page runs today — on the yard's machine (the Mac
here) only, in this version. The operator's "Mac only" meant that
machine as against a phone over the edge, not macOS as against Linux:
the rule is local-only, and a yard on another system would run a page
on its own machine the same way. The sandbox origin is a separate
local listener the edge does not carry (section 2), it admits no
acting user, and no run address is minted for a remote reader. A run
address is therefore what a note's run address is today: a loopback
address holding the page's hash, offered by the local viewer page,
serving whoever on that machine holds it. That is a capability of the
yard's machine, not a boundary surface, and the design does not
pretend otherwise; there is no remote reader in this version. Serving
it needs one read the boundary does not have: a private artifact's
blobs are exactly the ones `NoteBlob` now skips, so the sandbox
listener gets its own store lookup, beside `NoteBlob`, that answers a
blob any note names — an artifact's notes included — and is asked
only by `handlePagesBlob` on the pages listener. The main view's blob
route and every device route keep asking `ArtifactBlob` and the
skipping `NoteBlob`, so the hash alone still buys nothing there; what
admits the run is holding an address on a loopback listener the yard's
own host fence (and, in JWT mode, the gateway's token check the
listener already makes) already confines to that machine.
Running a page is trusting its author, as `docs/web.md` already says
of a note's page. The yard must say plainly, in the user
documentation, what section 2 states: the sandbox withholds the yard's
cookies, storage and API and refuses fetches, frames, workers, forms
and popups, but a page run top-level can navigate itself away with its
own contents, and anything typed into it, in the address, and no
released browser blocks its WebRTC connections. Serving bytes to a
reader always lets that reader keep and copy them; running goes
further, and lets the page itself send them out. No stronger promise
is made for artifacts than is made for a note's page today.

Nothing in this version is served through the edge. The two pages,
their blob routes and the viewer are reached only on the yard's
machine, in its local web view, and footplate in this version is the
desktop client on that same machine (section 3.6). No artifact page
is promised at an edge address — not for a yard the edge reaches by
its outbound link, and not through the existing hosted (VPS) web
path either — and no phone reads an artifact over the edge. What a
later edge version would show, when the router work of section 4
lands, is the page inert and dressed: its words, inline style and
`data:` images whole, no script running, and no run link; that is
named there, not promised here. No tokenless path is served in the
first version: a link for an outsider is following work (section 4).

### 3.4 How an agent makes and updates one

One new command group, `yardr artifact`, a thin command in
`internal/cli` registered in `root.go` as its neighbours are, over
calls that already exist:

```text
$ yardr artifact publish report.html --title "September in review" --note "first draft"
artifact yardr-a81c, version 1: http://127.0.0.1:8791/artifacts/yardr-a81c

$ yardr artifact publish report.html --id yardr-a81c --note "September figures corrected"
artifact yardr-a81c, version 2: http://127.0.0.1:8791/artifacts/yardr-a81c

$ yardr artifact list
yardr-a81c  September in review  v2  for ada  2026-10-26 09:41

$ yardr artifact show yardr-a81c

$ yardr artifact share yardr-a81c
artifact yardr-a81c: shared with the yard's users on their devices

$ yardr artifact unshare yardr-a81c
artifact yardr-a81c: private to ada again
```

`publish` without `--id` creates the artifact bead (type `artifact`,
originator from `--for` or the publisher's own bead's originator, as
`bead create --for` already decides it) and puts the first version;
with `--id` it adds one version note to that bead. Further files of
the same version go with repeated `--file <path>`; the first
positional file is the page or file the version is headed by. The
printed address is the view's, built from the view's configured
address as footplate builds a page's URL today, and is printed
whether or not the view is running — starting it stays
`yardr web start`, said in `show`'s output when nothing answers. An
agent in a bead session publishes the same way from its pane; where
the session's bead differs from the artifact, `publish` records that
bead on the artifact itself (the metadata `artifact_from`, written
once at creation by publish's own write — a generic bead update
carrying `artifact_from` for an artifact is refused beside the label
guard of section 3.2, so the backlink cannot be repointed as a side
effect — and shown by `show` and the viewer, so any reader of the
artifact can name the bead that made it), records that same bead on
the version note itself (`Note.Ref`, section 3.1; a later version
published from another bead records that bead on its own note, and
`artifact_from` still names the first) and leaves a one-line
pointer note on the session's bead, so the work's own record points
at what it produced: the same pointer shape as a comment's
(section 3.5), its text saying only that the bead published an
artifact and which version, with the artifact named in its
`Note.Ref` and nowhere in rendered words. Every pointer note whose
`Note.Ref` names an artifact — this publish pointer and a comment
pointer alike — is omitted at the boundary for users holding no leg
to that artifact: on the publishing bead's web and device bead
pages, in both boundary event streams, and in home's feed
(section 3.2).
Updating is publishing
again: versions are kept, the id stays the same, and there is no edit
of a published version — a correction is a new version, as a
correction to a sent letter is a new letter. `share` and `unshare`
are the only commands that write the `artifact:shared` label of
section 3.2; any in-yard caller may run them, the write records who
did, and they govern devices and the web view only, never readability
inside the yard. Erasing one is `yardr artifact delete <id>`
(section 3.7).

There is no link command in the first version: a link for an
outsider is following work (section 4), with its commands, record
and settled rules named there, and `share` never stands in for it.

### 3.5 Comments and feedback back to the agent

A comment is a note with no files on the artifact bead, written from
the viewer page (where the view acts as a user), from footplate, or
with `yardr bead note <id> "<words>"` — no new call, no new thread
kind. Inside the yard anyone who can call the store may comment, as
on any bead; at the boundary, the viewer page and footplate offer
commenting only to a user who may read the artifact there
(section 3.2). The artifact itself names its maker: `artifact_from`
(section 3.4) stands on its record and in its viewer, so a reader is
never left hunting for the bead that published it.

Feedback reaches the maker in the first version by one stated route,
and its limits are part of the proposal. The comment stands on the
artifact for good, and whenever a comment is written — by any path,
the command line, the viewer or a device — the store also leaves one
pointer on the bead that published the artifact's latest version,
written through `AddNotedAs` so that bead's live session, where it
has one, is told once (`Note.Notify`), exactly as `yardr bead note`
tells a session today. The bead is read from the newest file-carrying
note's `Note.Ref` (section 3.1): while an artifact has one version
that is its maker, the bead `artifact_from` names; when a later
version was published from another bead, comments wake that bead, the
one whose work the reader is looking at, and the earlier publisher is
not woken for words about a version it did not make. Where the
latest version records no bead (published from a terminal), the
pointer falls back to `artifact_from`, and where that names nothing
either, no pointer is left and the comment simply stands. Putting
the pointer in the store's comment path, rather than in each client,
is what makes the device path wake the maker: footplate keeps calling
plain `AddNote`, and the wake happens underneath it.

The pointer must not carry the private artifact across the boundary
on the back of its publishing bead. A work bead's page and event
stream are boundary-visible to users who may have no leg to the
artifact it published, so a pointer that named the artifact or quoted
the comment's words would tell exactly those users that a private
artifact exists and what was said on it. The pointer therefore
carries no comment words and no artifact title in its text: it stays
a plain note (so `Note.Notify` wakes the session exactly as today,
which a new note kind would forfeit — `addNotedNotifySession` in
`internal/store/graph.go` clears the wake for every kind but a plain
note), its one line of text tells the publishing bead's own readers
that a comment arrived on an artifact it published, and the
artifact's id is held in one new field on the note record
(`Note.Ref`, the handle the note points at), not in words a page
renders. A note is a pointer note exactly when its `Ref` names an
artifact bead, whether it is the publish pointer of section 3.4 or
the comment pointer above. The boundary surfaces resolve that reference before
they show anything: the web view's generic bead page and the device
bead call omit every such pointer note, and the web and device event streams
omit its `noted` event, for an acting or device user holding no
boundary leg to the artifact it names (the same check as
section 3.2, applied to the pointer's reference), while a user
holding a leg, and every reader inside the yard, sees the pointer
whole and follows it to the artifact. Inside the yard the pointer is
an ordinary readable note, as every note is; the omission is a
boundary rule only, like the rest of section 3.2.

Where the publishing bead has no live session — the
work ended, or the artifact was published from a terminal with no
bead behind it — nothing wakes: the comment waits on the artifact and
on the publishing bead's record, and the maker reads it in
`yardr artifact show`, in the bead's notes, or on their next session
on that bead. A comment is not mail: it reaches no inbox, and a
dedicated "send feedback" that mails the publisher is a convenience
for a later version, not a mechanism the first version needs
(section 4).

### 3.6 What footplate shows

Footplate gains an artifacts list and opens the same viewer, in the
depot `footplate`. The phone client (`footplate-android`) is out of
the first version by the operator's answer: desktop first, and
Android follows as its own bead after the first version lands
(section 4). A paired
client may call only what `docs/api.md` promises (`Calls` in
`internal/rpc/api.go`), which has no bead listing, so the list is one
new public call, `Artifacts`, with the one argument `viewer`: it
answers the rows of the artifacts that viewer may read at the
boundary (section 3.2) — each row the artifact's id, title,
originator, current version number, when that version was published
and its author's name, newest first. The store derives versions from
the bead's file-carrying notes as section 3.1 derives them, so no
second version record exists; the yard's listener serves the call for
the device's own user, ignoring any viewer the client names, as
`UserAsks` is served there today (`internal/rpc/user.go`). A row
opens the artifact through the `Bead` call the client already has —
which the device listener now answers for an artifact only to a user
holding a boundary leg (section 3.2), so the public call and the
filtered list cannot disagree — whose view carries the version's
files and the `artifact_from` backlink the viewer shows, and each
file's bytes through the blob route as a note's file comes today; it
opens as footplate opens a note's page today — images drawn inline
where the client already draws them, a page handed to the system
browser at the view's address (`page_url` in footplate's
`src/api.rs`), which is the inert dressed page and
is never offered as a run, other files downloaded by the browser.
That open is local and bare, with no identity handoff in this
version: no token and no signed URL travels with it. Footplate is
the desktop client on the yard's machine, and the URL it opens is
the local view's bare address, so the browser is served as the
view's acting user — the user the view was started to act as
(`--as`, held by its cookie, or the verified user in JWT mode;
`acting` in `internal/web/server.go`, reported by the view itself
as `As` on its home answer) — and for no one else. Footplate
therefore opens a page only when its own device user is that acting
user: it compares the two before it opens, and when they differ it
lists the artifact still (the list is served for its device user)
but does not open the page, serving no blob for it, and says to open
it as that acting user in the view instead. A safe handoff that
would let a different device user open the page is left out
(section 4). Commenting is the note box footplate already has, over `AddNote`;
the bead the latest version records is woken underneath that call by
the store's pointer note
(section 3.5), so no client change carries the wake. No embedded web
engine and no preview rendering are proposed, and no remote open is
promised: a footplate paired from another machine lists through its
device calls, and opening the page in a browser there waits for the
edge work of section 4.

### 3.7 Size limits and retention

The file limits are the blob limits, unchanged: one file at most
`blob_max_bytes` (8 MiB unless the operator sets it), one version at
most `MaxNoteFiles` (64) files, refused whole as a note with too many
files is refused today. Two further rules are the operator's settled
answers, enforced in the store where a version is written (bead 1): one artifact
holds at most 100 versions — the 101st version refuses, naming the
count and the cap, while a comment on a full artifact still stands.
The cap stands in the store's one note write path
(`addNotedNotifySession` in `internal/store/graph.go`, through which
`AddNote`, `AddNotedAs` and `artifact publish` alike write), counted
over the artifact's file-carrying notes at the moment of the write,
so no caller can mint a version around it: `yardr bead note --file`
on an artifact at the cap refuses exactly as `artifact publish`
does, and a fileless note is never counted and never refused by it —
and retention is the note rule with no clock on it: a version's blobs
live while its note stands, an artifact lives until it is erased
(below), and nothing erases by age. Closing its bead ends publication
but keeps the bytes readable, as a closed bead's notes stay readable,
so a report can be reopened next quarter; a closed artifact is kept
until its originator or an operator erases it.

Erasure is new, because nothing today removes a bead (section 2):
`yardr artifact delete <id>` erases one artifact outright. Only its
originator or an operator may erase it — the operator's settled
answer — asked at the command as the yard asks who may do what
elsewhere. In one write the store removes
the bead's row and handle and all its notes; it refuses, naming the blocker, while a session
names the artifact (none should: an artifact is never routed), so no
yard record is left pointing at nothing and the operator removes the
pointer first. No dependency edge can block it, because no edge may
name an artifact in the first place (section 3.2). The pointer note
that `publish` leaves on the publishing bead (section 3.4) is not
such a pointer: it is a note in that bead's history, notes have no
delete operation, and it stays after erasure as a tombstone — inside
the yard it still names, by its `Note.Ref`, what was published and
that it is gone, while at the boundary it is omitted for every user,
since no leg to an erased artifact can be held (the same rule as its
retained events, below) — and `show` and `delete` on the
erased id answer that the yard has not got it, tombstone
notwithstanding. The version blobs are then named by nothing, and
`pruneBlobs` reclaims them at the next `Prune`, past the usual grace
for a blob just put (`BlobGrace`). What erasure does not rewrite is
the event log: the events that recorded the artifact's creation and
notes remain until the log's own retention prunes them
(`DefaultRetention` in `internal/store/prune.go`), holding sequence
numbers and note references, not readable versions. Those retained
events are a boundary problem of their own: the creation event
carries the artifact's title, type and originator (`createdData` in
`internal/store/beads.go`), and the boundary streams of section 3.2
classify an event by the bead it names — a classification that would
have nothing left to classify once the bead row is gone, so an erased
private artifact could still be identified on a stream that replays
or trails its old events. Erasure therefore leaves one filterable
tombstone behind: in the same write it records the artifact's id in
a small erased-artifacts bucket with nothing but the id and the
erasure time — no title, no originator, no file, no note — and the
boundary stream filters treat an event naming a tombstoned id
exactly as an event naming a private artifact, for every user,
since no leg to a gone artifact can be held. The tombstone is
pruned with the events it classifies, under the same
`DefaultRetention`, so it cannot outlive the log it guards; inside
the yard it changes nothing, and the local socket's stream answers
as it does today. After erasure
the id answers as a bead the yard has not got to everyone. Erasure
is for this type alone: a work bead carries dependencies, sessions
and the yard's memory of what was done, and no general bead deletion
is proposed (section 4).

## 4. Left out

- **Per-agent privacy inside the yard.** An artifact, like every bead
  today, is readable by any agent or process that can call the store:
  no viewer is resolved from a caller's pane, no read is filtered by
  who asks, and the local socket's event stream is not filtered for
  artifacts (the web view's and devices' streams are boundary surfaces
  and are filtered, section 3.2). Making one bead type private to
  some agents while every other bead stays open would be a special
  case on the yard's central record, and the question of which
  agents may read which beads belongs to all beads at once, as a later change designed for
  the whole store — its reads, its event stream and its socket
  together — not to artifacts alone. Visibility in this design
  governs only the outside boundary (section 3.2), where the yard
  already knows which user a device or view acts for.
- **Runtime capabilities.** A published page runs only its own inline
  script under `scriptedPolicy`; it fetches nothing, holds no shared
  state, and calls no yard API. Live data would need an origin that
  may call the yard for one artifact's readers, which is a security
  design of its own. The bead names this out of scope for the first
  version, and nothing above leans on it.
- **A page's own files beside it.** A page that loads sibling files by
  relative path (its own stylesheet file, script file or image file
  beside it) is not served as a site: the sandbox policy refuses those
  loads, and serving a directory would need path rules, an index rule
  and a second reading check per asset. What the first version serves
  instead is the yard's one shared stylesheet and script injected
  inline into the page at serve time (section 3.3), which the page
  overrides inline; per-page files are the follow-up if shared dress
  proves too plain. One
  self-contained page per version, plus standalone files in the same
  version, covers the report, mock and screenshot-set cases the bead
  names.
- **Running a page over the edge, or anywhere remote.** A run needs
  the sandbox origin, which the edge does not carry and which admits
  no acting user (sections 2 and 3.3); carrying it would mean minting
  run addresses for remote readers and answering whose page may run
  there, for a phone that reads a report as well inert. Running waits,
  if it is ever wanted, for a design that names its reader.
- **Minting or revoking a link from a device.** A person mints at a
  terminal, an agent through an approved run card (section 3.2);
  footplate shows links' standing nowhere in the first version. A
  device surface for links would put a bearer capability's minting
  behind a device token, which wants its own weighing.
- **Editing or deleting a version in place.** Blobs are written once
  and a note is the record; a mutable version would fork both. A
  correction is a new version. Whole-artifact erasure (section 3.7)
  is the only removal, and it takes every version together.
- **Deleting beads in general.** Erasure is specified for artifacts
  alone. A work bead's record is the yard's memory and other records
  point at it; deleting one is an audit and referential-integrity
  design of its own, for no need this bead names.
- **A run that cannot carry its page away.** No browser policy the
  yard can set today blocks self-navigation or WebRTC for a
  top-level page (section 2); a stronger guarantee would need the
  page framed, proxied or rewritten, each a serving design of its
  own. Run carries the same residual as a note's page, stated
  plainly, and is never promised to contain a page.
- **A separate artifact table, store or service.** The bead, note
  and blob mechanisms already give identity, versioning, storage and
  pruning; a parallel table would need its own history, its own
  prune and its own client surface for no behaviour the first
  version names. (The following link work in the next bullet does
  need one new record — an expiring outsider capability no existing
  record is — and says so there.)
- **An embedded viewer in footplate.** The desktop and phone clients
  draw images themselves and hand pages to the system browser today;
  embedding a web engine to render a page inline is a dependency and
  a sandbox of its own, for a nicer frame around the same bytes.
- **Mail feedback, digests and a feedback inbox.** A comment wakes
  the publishing bead's live session through the pointer note
  (section 3.5) and otherwise waits on the record; it reaches no
  inbox. Mailing the maker, or a page of comments across artifacts,
  waits until comment volume shows the wake is not enough.
- **Artifacts in footplate-android.** The operator's answer is
  desktop first: the phone client gains no artifact list or viewer in
  the first version. Android is its own bead
  after the first version lands, over the same public calls bead 3
  uses, and wants no store or serving change this design has not
  already specified.
- **Edge pages, and links for outsiders.** The first version serves
  nothing through the edge at all: not a connected yard's artifact
  pages, not the existing hosted (VPS) web path for an artifact, and
  not a remote paired footplate opening a page in its browser. Two
  pieces of following work, each its own bead after the first version
  lands, take an artifact further. In the `yardr-hosted` depot, the
  router learns to carry a connected yard's web view (it forwards that
  yard's device calls today but not its web view, and refuses a
  request with no account session but for its landing page,
  section 2), so the pages of section 3.3 reach a phone as the inert
  dressed page; the hosted (VPS) web path and a remote paired
  footplate's page open ride on that same router work, or on a bead
  that names them, and are not promised before it. In this
  depot, an outsider link is built on the record nothing existing
  provides — an expiring, revocable, single-artifact capability:
  a random token of at least 32 bytes shown once at minting, only its
  SHA-256 hash kept with the artifact's id, the version it serves
  (pinned by `--version`, otherwise following the current version),
  an expiry and its maker's name; `/shared/<token>` in the yard's web
  view serves that one artifact inert and nothing else, checking
  expiry and revocation on every follow. Its settled rules, decided
  with the operator and held here so the bead starts decided: the
  minting words say a holder can keep and copy what they are shown;
  a link is made by a person, or by an agent only through a weighty
  run card naming `yardr artifact link` that the operator approves
  (the mint write refusing an agent session, the approval running it
  as the approver, who is shown the address once); a link lasts seven
  days by default and thirty at most; anyone in the yard may revoke
  one by deleting its record; `link|links|unlink` stay distinct from
  `share|unshare`, and erasing an artifact removes its link records
  with it. Until the router bead lands, that link answers only where
  the yard's view already reaches. Footplate is unaffected by either
  piece: its device calls are already carried.
- **A safe identity handoff from footplate to the browser.** Desktop
  footplate opens the bare local view URL with no token and no signed
  URL (section 3.6), so it can open a page only when its device user
  is the view's acting user. A handoff that carried the device's
  identity into the browser — a short-lived signed URL, a token the
  view exchanged for its cookie, or a browser session minted for the
  device user — would let footplate open a private artifact for a
  different device user, and wants its own weighing of what such an
  address may reach and for how long.
- **The hosted product.** Nothing here changes what a hosted yard
  offers, bills for or keeps.

## 5. Open questions

None. Every question this design carried to the operator has been
answered on the bead (26 Oct 2026), and the proposal above builds the
answers, each where it belongs: versions are capped at 100 an
artifact, enforced in the store's note write path with its refusal
so no caller mints a version around the publishing command
(section 3.7, bead 1); a closed artifact is kept until it is erased, and nothing
erases by age (section 3.7, bead 1); sharing with the yard's users is
anyone's in the yard through the share command and never by merely
setting a label (section 3.2, bead 1); a page's scripts run on the yard's machine (the Mac
here) only — local as against a phone over the edge, not macOS as
against Linux (section 3.3, bead 2); a page is one self-contained file dressed by
the yard's shared stylesheet and script injected inline ahead of its
own, which its inline style and script override, with files of its
own beside it left out (sections 3.3 and 4, bead 2); the first
version is local first — the yard's machine and desktop footplate
on that machine, with nothing served through the edge (not a
connected yard's pages, not the hosted (VPS) web path, not a remote
paired footplate's browser open) and outsider links out of it as
their own following beads, whose rules are already settled and held
in section 4 (a link made by a person or by an agent only through an
approved run card, lasting seven days by default and thirty at most,
revocable by anyone in the yard); only an artifact's originator or an
operator may erase it for good (section 3.7, bead 1); footplate is
desktop first, with footplate-android out of the first version as
its own later bead (sections 3.6 and 4, bead 3); and footplate opens
a page with no identity handoff — it opens only when its device user
is the view's acting user, and otherwise lists without opening
(sections 3.6 and 4, bead 3). Further
points in this draft are requirements the critiques established, not
choices: the device bead call and event stream, and the web view's
generic bead page and event stream, apply the boundary rule to
artifacts (section 3.2, bead 2); home's feed, on its first and paged
pages, in HTML, JSON and `?all=1` alike, drops an artifact's events
for a viewer with no leg before any entry is built from them
(section 3.2, bead 2); no dependency edge names an artifact at either
end, `artifact_from` being a field and never an edge (section 3.2,
bead 1); a private artifact runs on the yard's machine through a blob
lookup only the sandbox listener asks, the hash alone still buying
nothing on the view or on a device (section 3.3, bead 2); a comment
carries its backlink and wakes the live session of the bead that
published the latest version where it has one — the maker
`artifact_from` names while there is one version — through a pointer
that carries no comment words at the boundary, and every pointer
note whose `Note.Ref` names an artifact — publish and comment alike
— is filtered the same way on the bead pages, both streams and
home's feed (sections 3.1, 3.2, 3.4 and 3.5, beads 1 and 2); an erased artifact stays unidentifiable on the
boundary streams under its erasure tombstone (section 3.7, beads 1
and 2); and the shared dress is injected inline rather
than linked, because a linked stylesheet or script cannot load on
the sandboxed run origin (sections 2 and 3.3, bead 2). What a later
draft may reopen — running over the edge, per-page files, per-agent
privacy — stands in section 4 with its reasons.

## 6. The beads it would become

In the order they would land. Each is sized so
a builder finishes it without asking; "done when" is the test of
each. After these three, the following work named in section 4 —
edge pages in `yardr-hosted` (connected yards, the hosted (VPS) web
path and a remote paired footplate's browser open), an outsider link
in this depot, a safe footplate-to-browser identity handoff, and
footplate-android — is filed as its own beads; none of it is part of
the first version, and none is numbered here.

1. **Hold artifacts in the store and publish them from the command
   line.** Goal: `TypeArtifact` in `NotWork` with stage `artifact`;
   versions derived from file-carrying notes and comments from
   fileless ones (section 3.1); `yardr artifact
   publish|list|show|share|unshare|delete` over `CreateBead`,
   `PutBlob`, `AddNote`, the dedicated share write of section 3.2
   and the erasure of section 3.7; `artifact_from` recorded at
   creation, each version note recording its own publishing bead in
   `Note.Ref`, and the publish pointer note on the publisher's bead,
   which stays as a tombstone after erasure; the comment pointer of
   section 3.5 in the store's note path (`Note.Ref` naming the
   artifact, no comment words in its text), left on the bead the
   latest version records; the edge refusal of section 3.2 in the
   store's one edge write; and the settled limits of
   section 3.7 — the 100-version cap enforced in the store's one
   note write path, closed artifacts kept until erased, and the
   erasure tombstone of section 3.7. Where to look:
   `internal/store/beads.go` (`NotWork`, `TypeJournal`, the field
   guards `checkFlowChange`/`checkMergeOK` for the label guard, and
   `UpdateBead`/`CreateBead` where labels land),
   `internal/store/blobs.go` (`PutBlob`, `MaxNoteFiles`,
   `pruneBlobs`), `internal/store/graph.go` (`AddNote`,
   `AddNotedAs`, `Note.Notify`, and `AddDep`/`w.addDep` for the edge
   refusal), `internal/store/prune.go` (what
   `Prune` keeps), `internal/cli/bead_note.go` for `--file`,
   `internal/cli/root.go` for registration, and a deletion neighbour
   such as `DeleteUser` (`internal/store/users.go`) for the
   erasure's shape. Done when: store and command tests show an
   artifact bead is never ready and never routed; a first publish
   is version 1 under a new id and a second publish version 2 under
   the same id; a fileless note changes no version; an over-cap
   file or a 65th file refuses whole; publishing the 101st version
   refuses, naming the count and the cap, a version minted around
   the command by `yardr bead note --file` (or any `AddNote` caller)
   on a full artifact refuses the same way, and a comment on the
   full artifact still stands; `share` and `unshare` write and clear
   the label and record who did, a generic `UpdateBead` or
   `CreateBead` carrying `artifact:shared` or `artifact_from` for
   an artifact is refused, and no other command in the group
   writes the label;
   publishing from a bead session records `artifact_from` and shows
   it in `show`, each version note records the bead that published
   it in `Note.Ref` (a second version from another bead records that
   bead, the first still naming the maker), an edge of any kind
   naming an artifact at either end — by `AddDep`, by a
   discovered-from record at creation, or by folding — is refused
   naming the artifact, while `artifact_from` itself is written and
   read as a field throughout, a comment by any path also leaves its
   pointer note through `AddNotedAs` on the bead the latest version
   records — on the maker while there is one version, on the second
   version's bead once one stands — its text carrying no comment
   words and no artifact title, its `Note.Ref` naming the artifact —
   and tells that bead's live session
   once, and a comment where no bead or no live session stands
   wakes nothing and still stands; erasure by the originator or an
   operator removes the record and its notes, refuses while a
   session names the artifact, leaves the
   publishing bead's pointer note standing as a tombstone, records
   the erasure tombstone of section 3.7 (the id and nothing else),
   and
   leaves its blobs to the next prune, which keeps a standing
   version's blobs until then; a non-originator who is no operator
   is refused erasure; a closed artifact ages unerased; and
   `--json` holds on every command. Depends on nothing.
2. **Serve artifacts at the outside boundary: the web view, device
   paths and the public list.** Goal: the boundary rule of
   section 3.2 — `ArtifactBlob(ctx, viewer, hash)` beside `MailBlob`,
   `NoteBlob` skipping blobs named only by artifact notes, the
   `/artifacts` and `/artifacts/<id>` pages with the acting user as
   viewer on every read behind them, `userBlob` answering an
   artifact's blob to its reader's device, the device listener's
   bead call and `/user/events` stream applying the same legs to
   artifacts, the web view's generic `GET /beads/<id>` page and its
   `GET /events` stream applying the same legs to artifacts, home's
   feed (`Server.feed`, first and paged pages, HTML, JSON and
   `?all=1`) dropping an artifact's, a pointer's and a tombstone's
   events for a viewer with no leg before any entry is built, the
   sandbox listener's own blob lookup of section 3.3 (asked only by
   `handlePagesBlob`) so a private artifact runs on the yard's
   machine, the
   shared stylesheet and script of section 3.3 injected inline into
   every artifact page ahead of the page's own inline style and
   script with no policy or header change, run offered only from the
   viewer on the yard's machine, and the public `Artifacts` call of
   section 3.6 in `Calls` and in `docs/api.md`, served by the
   listener for the device's own user. Where to look:
   `internal/store/blobs.go` (`NoteBlob`, `MailBlob`),
   `internal/web/server.go` (`routes`, `acting`, `PagesHandler` and
   its `Cross-Origin-Resource-Policy`), `internal/web/beads.go`
   (`handleBlob`, `handlePagesBlob`, `beadPage`, `pagePolicy`,
   `scriptedPolicy`), `internal/web/events.go` (`handleEvents` and
   its frame), `internal/web/home.go` and `internal/web/feed.go`
   (`handleHome`, `Server.feed`, `keptHome`, `reader.entry`) for the
   home filter, `internal/cli/web.go` for the local listeners,
   `internal/web/assets` for the
   shared dress, `internal/store/decisions.go` (`Bead` for the shape
   of a client read), `internal/rpc/api.go` (`Calls`),
   `internal/rpc/user.go` (`UserServer.bead`, `deviceEvent`,
   `userBlob`, `asUser`), and `go generate ./internal/rpc` for the
   socket client. Done when: web and RPC tests show a private
   artifact listed and served to its originator's view and device
   and to no anonymous view; a shared artifact served to another
   named user's view and device; a direct device `Bead` read of a
   private artifact by another user answering as a missing bead, and
   that device's `/user/events` stream carrying no event that names
   the artifact, while the originator's device is told both; another
   named web user opening `GET /beads/<id>` of a private artifact
   getting a missing page — its title, notes, files and history
   nowhere in the answer — and that user's web `GET /events` stream
   carrying no event that names the artifact, while the
   originator's web user gets the page and the events; home's feed
   telling another named user and a view with no acting user nothing
   of a private artifact — no entry, title or note words for its
   creation or any version on the first page, a paged page, the JSON
   or `?all=1`, nor any publish or comment pointer or tombstoned event there — while
   the originator's home feed shows them; a private artifact's page
   running on the yard's machine from the local viewer's sandbox
   address, its artifact-only blobs served by the pages listener's
   own lookup, while the same hashes answer as missing on the main
   view and on another user's device; every pointer note whose
   `Note.Ref` names a private artifact — the publish pointer and a
   comment pointer alike — leaving no trace at the boundary on its
   publishing bead: another named user who may read that bead sees
   neither pointer note on its web page or device bead read, nor
   either pointer's event on the web or device stream, nor either
   pointer in home's feed (first, paged, JSON or `?all=1`), and no
   comment words anywhere, while the publishing bead's own boundary
   reader holding a leg and every inside-yard reader see both
   pointers whole; and an erased private
   artifact unidentifiable on the boundary streams — its retained
   creation and note events, title and originator in them
   notwithstanding, reaching no boundary user while its erasure
   tombstone stands, and the tombstone itself naming nothing but
   the id; an artifact
   page served dressed — the shared style and script injected inline
   ahead of the page's inline style and script, the page's inline
   words winning, no linked asset requested — opening inert on the
   view with no script running and no run link
   offered, and running only from the local viewer's sandbox
   address, the dressed run verified in a browser (a headless Chrome
   shot or driven check at the run origin showing the shared style
   applied, the page's inline override winning and the shared script
   running), not by handler tests alone; a file downloading;
   comments from the viewer standing on the bead; an unknown or
   forbidden id answering as a missing page; the public list
   answering only its viewer's boundary-visible artifacts with their
   current versions; a device naming another user as its viewer
   refused by the listener as today; generic note pages keeping
   today's policies and no injection; and reads inside the yard
   through the socket unchanged, an artifact's record, notes, files
   and local socket event stream answering as any bead's do.
   Depends on bead 1.
3. **Show artifacts in footplate, with comments.** Goal: the list
   and viewer of section 3.6 in depot `footplate` (desktop only;
   `footplate-android` is out of the first version and follows as
   its own bead, section 4), over the `Artifacts`, `Bead` and
   `AddNote` calls and `page_url`, with `artifact_from` shown as the
   maker's bead. Where to look: footplate's `src/api.rs` and its
   window's note and mail rendering. Done when: a published
   artifact lists on the desktop client on the yard's machine
   through the public call, its images draw, its page opens in the
   system browser at the local view address as the inert dressed
   page when footplate's device user is the view's acting user, a
   file downloads, a comment written there stands on the bead, wakes
   the live session of the bead the latest version records through
   the store's pointer note, and names that bead in the viewer; when
   footplate's device user is not the view's acting user, the same
   artifact still lists but footplate does not open its page, serves
   no blob for it, and says to open it as that acting user; an
   artifact private to another user never lists on this user's
   device and its direct `Bead` read there answers as missing; and
   no token or signed URL is minted for any open. Depends on bead 2.
