# `yardr web serve --ui <dir>`: the view serving another front end's files

Design yardr-zyzp, proposed 2026-10-11. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

Design for bead yardr-zyzp (flow `design`, stage `draft`). The web view is
`yardr web serve`, the read-only browser view of the yard
(docs/web.md); signalbox is the 3D yard, a second front end in
`~/projects/repos/signalbox`, read for this design but not changed. A yard
is one installation of yardr; the operator is the person who runs it; a
route is one URL the view answers. See docs/glossary.md for all of
yardr's words.

## 1. The question

Once a second front end reads the yard only through the web view's JSON
and stream, signalbox's own server, `scripts/serve.mjs`, is left doing
three things: serving `dist/`, relaying the view's routes, and cutting the
JSON down to the fields the page may see (`src/project.ts`). Should
`yardr web serve` serve another front end's static files itself,
`yardr web serve --ui <dir>`, so that no Node process runs at all?

## 2. What exists today

**The view's JSON is the yard's read API, and the routes this bead waited
for exist.** docs/web.md ("The lists") states the rule yardr-2wf5 landed
(8 Oct 2026, merged at b37ac65): whatever a list command prints with
`--json`, the view answers at a route of the same name, with the same
fields, rows of the same Go types, under a key named as the route beside
`head`, the yard's newest sequence number from before the read, so
`/events?after=<head>` continues the answer with no gap. The routes are
`/depots`, `/groups`, `/routes`, `/peers`, `/deps` (JSON only, no page;
handlers in internal/web/lists.go, each ending in the `listed` helper),
`/beads` and `/beads?all=1` (whole `store.Bead` rows, closed beads too),
`/sessions` and `/sessions?all=1` (the command's rows under `list`, ended
sessions too), `/crew`, `/flows`, `/beads/<id>` (with notes, sessions,
history and the stage file's whole text), and the stream
`/events?format=json` with `Last-Event-ID` resume. Still without a route:
`timer`, `hook`, `source`, `user`, `link`, `pack`, `thread`, `review`,
`flow list`, a bounded window of the log, and anything for `yardr limits`
(below).

**The guard the bead wants for the 3D page.** The view is read-only: no
page takes a write, and every route in `routes`
(internal/web/server.go) is registered as a GET route, so the mux refuses
every other method itself. With no authentication it listens on loopback
only; `--unsafe` opens every interface, still with no login. A request
whose `Host` header names no loopback name, IP address, machine host name
or `--host` name is answered 421 (`knownHost` in server.go). The open
`/events` streams are limited (16 per address and 128 in total off
loopback, 128 on loopback counted apart; constants in
internal/web/events.go). Every response carries
`Content-Security-Policy: default-src 'self'`, which refuses inline
`<script>`, inline `<style>` and anything off this server; a test fetches
every page and asset and fails on a URL naming another host
(`TestNoOtherHost`). Two corrections to the bead's account matter. First,
`yardr web install` and `yardr web start` are loopback-only and have
neither `--unsafe` nor `--host` (docs/web.md, "Keeping it running"), so
the login service covers a `--ui` page on this machine only; a page open
to the LAN would still be a hand-run `web serve --unsafe`, as tonight's
is. Second, a view does not replace itself when a new yardr is installed;
it serves until it is started again (docs/web.md, "Where it runs").

**signalbox already follows the view.** `scripts/yard.mjs` runs no yardr
command: it reads `/depots`, `/flows`, `/groups`, `/routes`, `/crew`,
`/peers`, `/beads`, `/beads?all=1`, `/sessions?all=1`, `/deps`,
`/beads/<id>` and `/events?format=json&after=<seq>` from the view named by
`YARDR_WEB`, and takes the yard's head from a list's `head`. Its
`src/project.ts` is the filter the bead describes, and it is an
allow-list: each shape is built with `pick` from the fields it names, so a
field yardr adds tomorrow is not passed on; bead bodies and notes leave
only in the card's answer; sessions are aliased by a hash, never named;
events keep a few string fields; the quota keeps each provider's name,
plan and two windows. Tonight, verified on this machine,
`node scripts/serve.mjs --host 0.0.0.0 --port 8792` runs by hand beside
`yardr web serve --unsafe`, as the bead says. But serve.mjs does more
than the bead's three things: beside `dist/` it answers `/api/snapshot`
(ten view routes projected and assembled into one answer, plus the
layout), `/api/feed` (a relay of the view's stream with per-listener
cursors, plus quota messages), `/api/bead/<id>`, and `POST /api/fault`
(browser fault reports, written to its log); it asks the `aiquokka`
binary for the providers' quota, cached at most once a minute
(`QUOTA_EVERY`), a snapshot waiting at most three seconds for it; and it
keeps the layout memory per yard served, at
`$YARDR_HOME/signalbox/layout.json`, placing and remembering slots on
every snapshot (signalbox-hx63, merged: the memory belongs to a yard, not
to the built page). The page itself calls these `/api/*` routes, not the
view's; it could not talk to the view today without a change of its own.
Its built `dist/index.html` carries no inline script or style (an external
module and stylesheet only), so the view's content-security policy would
admit it as it stands.

**What leaves the machine is now a concrete list.** The builder of
yardr-2wf5 recorded what the new list routes add to an off-loopback view,
and each item checks out in the code: depot `path` (and the `test`
command; `store.Depot` in internal/store/graph.go) at `/depots`; the
whole group `config` at `/groups`, which for an exec group names its
command, directory, log file and worktree directory and for a herdr group
its prompt file, env file and env command among others
(`ConfigKeys` in internal/store/configkeys.go); peer `key` and `url`
(beside calls, expose, roster and trust; `store.Peer` in
internal/store/peers.go) at `/peers`; every bead's `body` and metadata at
`/beads`; sessions' `handle`, a free-form map of agent names, pids and
pending prompts (`store.Session` in internal/store/routing.go), at
`/sessions`. The older routes add their share: `/flows` prints
`stage_file` paths (relative to the depot, `.yardr/flows/default/new.md`,
not under the home as the bead has it), and `/beads/<id>` prints the
stage file's whole text, every note, the bead's sessions and its history
with body diffs. This yard's view is open on the LAN tonight, so all of
this is readable off this machine now.

**The quota.** `yardr limits` asks the yard's declared source with a
`limits` key (docs/integrations.md), once per harness kind, now, as
whoever runs it; "this is a command for when you want to know, not one to
run in a loop" (internal/cli/limits.go). The answer is for the operator's
login on this machine. The view has no `/limits` route, runs no commands,
and remembers nothing between requests.

**Decided, not reopened.** mckean on networking, 9 Oct 2026 (memory
mem-stdz): yardr defines what to allow and does not handle proxies or
Tailscale ("not building nginx"); the view's loopback rule and `--unsafe`
stay as they are.

## 3. The proposal

`yardr web serve --ui <dir>` serves the directory's files at `/`, with
the view's JSON and stream routes unchanged. The existing mechanism
carries it: the mux in `routes`, where the most specific pattern wins;
the `ServeHTTP` guard (the 421, the content-security policy and the other
headers), which covers the files unchanged; and the record under
`<home>/webviews/` (internal/cli/webviews.go), which gains the directory
so `yardr status` names what a view serves. What is new is one handler,
one flag, and that record field.

Concretely, when `--ui` is given: a request for `/` still answers a JSON
client with Home, but a browser gets the directory's `index.html` instead
of the home page; a path no route answers serves the directory's file
there, where today the catch-all 404s; and `/assets/<file>` falls through
to the directory when the name is no asset of the view's, because a built
front end keeps its own `assets/` (signalbox's
`dist/assets/index-*.js`). Every route keeps both its answers, so the
front end reads `/events` and the JSON routes on its own origin and the
view's own pages stay reachable as they are; routes win over files, so a
file named `beads` is shadowed and stays shadowed, which the document
says. The handler answers GET (and HEAD, as the other routes do) and
nothing else; it serves nothing above the directory; it names its content
types in code as `handleAsset` does, never from the machine's tables; and
it sets no cache but `no-store`, as the JSON answers do. Refusals stay
the view's: a missing directory or a directory without `index.html` is
refused before the yard is touched, as a refused `--addr` is. `web start`
and `web install` take `--ui` too, the way they take `--addr` (the
`Flags` of `svcWeb` in internal/cli/longrunning.go), loopback-only as
they are.

A person would type, with a signalbox checkout beside the yard:

```sh
cd ~/projects/repos/signalbox && npm run build
yardr web serve --ui ~/projects/repos/signalbox/dist
```

and open `http://127.0.0.1:8791/`: the 3D page, reading `/events` and the
JSON routes on its own origin, under the view's 421, content-security
policy and stream limits, named by `yardr status` with the directory it
serves. Any other front end is the same flag with another directory.

This proposal is conditional, and the condition is the point: it is only
built once the exposure question below is settled, because with `--ui`
the page reads the view's JSON as it is and the project.ts filter has
nowhere to stand. Until that decision is implemented, the honest answer
to the question is nothing: signalbox keeps its Node server. The beads in
section 6 land in that order for that reason.

## 4. Left out

**The view's own pages under a prefix, or gone.** Falling through to the
directory only where no route answers keeps every page reachable without
moving a link; a prefix would rewrite the menu, the live reloads and the
tests that walk them, for no reader's sake. If the pages under `--ui`
ever confuse, that is a new bead.

**A Go port of serve.mjs's `/api/*`.** Assembling one snapshot, relaying
the stream per listener, and answering one bead's card are signalbox's
shapes, not the yard's; building them into the view would make yardr keep
one front end's server as a special case, against the general
`--ui <dir>` any front end uses. The page learns the view's routes
instead, on signalbox's side (bead 4 below).

**Layout memory and fault reports in the view.** Both need writes: the
slots are placed and remembered on every snapshot, and a fault report is
a POST. The view takes no write anywhere, and a first write route would
break the read-only rule the whole of docs/web.md stands on. Their new
homes, or their ends, are signalbox's to choose (open question 4).

**Caching the files.** Build outputs carry content hashes in their names,
so immutable caching would be easy; but `no-store` everywhere is correct
from the first day and the page is small. A builder may add it with the
bead, not as its own.

**A content-security policy per `--ui`.** The view's policy admits
signalbox's build as it stands, and one policy for every front end keeps
the "no other host" promise checkable. A front end that needs more (an
inline bootstrap, a font) changes its build, not the view.

**Single-page fallback.** An unknown path stays a 404; it does not serve
`index.html`. The page is one page with no router, and a fallback would
hide mistyped JSON routes behind HTML.

## 5. Open questions

Each is the operator's to decide; the costs are as they read from the
code above, and the recommendation is mine.

**1. What may `--unsafe` print?** Nothing else here moves until this is
settled, and it wants settling whether or not `--ui` is built: the list
in section 2 is readable off this machine tonight. Three options. (a) An
allow-list per route under `--unsafe` only: loopback keeps the whole
answers the view's own pages and local clients read, and the network gets
the filtered shape, pages and JSON alike (filtering JSON alone would
leave the same fields a click away). Cost: two shapes of one route for
clients to meet, and the stream's JSON events need the same filter, since
they carry whole event data. (b) The filtered shape everywhere: one shape
to keep. Cost: it guts the view's own pages on loopback too — a bead's
page without its body and notes is no page for a person — so this option
really means rewriting the pages around links the filter keeps, a larger
design. (c) Nothing: the Node server stays the filter, which is the
bead's option 3. Cost: two processes, two ports, and tonight's hand-run
relay with no guard stays the way the 3D page is served. I would take
(a): the loopback view already is the operator's full window, and the
network view becomes a deliberate smaller one, in the spirit of mem-stdz
(define what to allow). Its shape — which fields per route — is a fuller
decision than this design, and wants mckean's eye on the actual lists.

**2. `--ui` at `/`, under a prefix, or nothing?** The bead's three
options. The proposal above is option 1 with the own pages kept as they
are. A prefix (`/ui/`, option 2) keeps `/` the view's home at the cost of
a page that must know its base (signalbox builds relative URLs, so it
can, but every other front end must too) and an origin that still is not
quite its own. Nothing (option 3) is tonight carried on. I would take the
proposal once question 1 is implemented, and nothing until then, as the
yardmaster already saw: 3 until the exposure question is settled, then 1.

**3. Does the view answer the quota?** The page shows quota from a
`/limits` route or shows none: the browser cannot run aiquokka, and a
per-request run would put a network call per provider behind every page
read, against the command's own "not one to run in a loop". Options: (a)
a `/limits` route answering the declared source with a cache of about a
minute, as serve.mjs keeps (`QUOTA_EVERY`), errors as nulls, not as
failures of the page. Cost: the view runs commands for the first time,
and the answer describes the operator's login, so its `--unsafe` shape
belongs to question 1. (b) No route: the `--ui` page has no quota until
someone wants it enough to pay (a). I would take (a), after question 1,
because quota is the one thing in the snapshot that is not the yard's
JSON today and the page is poorer without it — but (b) blocks nothing
else, and is a fine first year.

**4. Where do the layout memory and the fault post go?** They cannot live
in the view (section 4). Options for the slots: (a) the browser keeps
them (local storage): cost is the end of the hx63 property, since the
memory belongs to a browser then, not to the yard — two browsers seat
differently, and a fresh one starts from slot 0; (b) signalbox keeps a
small writer beside the view for the slots alone, which is most of a
serve.mjs again and keeps a second process; (c) the page lays out from
nothing on every load and takes what comes. For faults: keep the post
against a tiny receiver, or drop it; it is best-effort logging either
way. I would take (a) for the slots with (c)'s acceptance behind it, and
drop the fault post: the memory degrades into a per-browser seat plan,
which for a wall display and a phone is what each wants anyway — but this
is signalbox's design to do, not yardr's, and its bead (bead 4) may come
back with a better shape.

## 6. The beads it would become

In the order they would land. Each is yardr's except bead 4, which is
signalbox's and is proposed here, not created.

**1. Filter the view's answers under `--unsafe` to an allow-list per
route.** Goal: implement the decision of open question 1, option (a): off
loopback, every page and JSON answer, and the stream's JSON events, carry
only the fields mckean allows; loopback is byte-identical to today. Where
to look: `respond`, `data` and `listed` (internal/web/server.go,
internal/web/lists.go) for the answers, `handleEvents`
(internal/web/events.go) for the stream, and section 2's list for what
each route sheds. Done when: the routes' `--unsafe` answers are asserted
field by field in internal/web tests, the loopback answers are asserted
unchanged, and docs/web.md names per route what the network gets. Depends
on: this design's decision. Nothing below lands before it.

**2. Answer the quota at `/limits`, cached.** Goal: the view's way to
`yardr limits`: one route answering every answered kind's report, asking
the declared source at most once a minute and serving the last answer in
between, errors as nulls. Where to look: `limits.Ask` and `Answerers`
(internal/limits, internal/cli/limits.go) for what an ask is,
`QUOTA_EVERY`/`QUOTA_WAIT` (signalbox's scripts/serve.mjs) for the
periods, `listed` for the route's shape (`{"limits": [...], "head": N}`).
Done when: `/limits` answers without running the source twice in a
minute however often it is read, a failing provider nulls its entry
without failing the route, and docs/web.md has the route's row. Depends
on: bead 1 for its `--unsafe` shape (the report describes the operator's
login); its loopback shape is this design's.

**3. Serve another front end's files: `web serve --ui <dir>`.** Goal: the
proposal of section 3: the flag, the fall-through handler, the directory
in the webviews record and in `yardr status`, `--ui` on `web start` and
`web install`, docs and tests. Where to look: `routes` and the catch-all
(internal/web/server.go), `handleAsset` (internal/web/assets.go) for
content types, `announceWeb` (internal/cli/webviews.go) for the record,
the `Flags` of `svcWeb` (internal/cli/longrunning.go) for start and
install. Done when: a fixture directory's `index.html` answers at `/`
for a browser while `/` still answers Home to JSON, unknown paths serve
files and 404 past the directory, `/assets/<view-file>` still answers the
view's, nothing above the directory is reachable, GET alone is answered,
and `TestNoOtherHost` still passes. Depends on: bead 1. Wanted by: bead
4; bead 2 for full parity of what the page shows.

**4. (signalbox) The page reads the view directly.** Goal: signalbox
without its own server: the page fetches the view's routes on its own
origin, projects them for display, and settles the layout memory and the
fault post as its own design chooses (open question 4). Where to look:
`scripts/yard.mjs` (which view routes carry every field),
`src/project.ts` (the allow-list, which becomes display logic: with the
filter in the view, the page needs no secrecy of its own),
`src/main.ts` (the `/api/*` calls to replace), serve.mjs's `place` and
`remember` (the memory to rehome). Done when: the built page served by
`yardr web serve --ui dist` draws the yard, follows `/events`, opens a
card per bead, and keeps its seats to its design's satisfaction, with no
Node process running. Depends on: beads 1–3.
