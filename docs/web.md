# The web view

`yardr web serve` shows the yard in a browser. It is a client of the yardr
server, as `yardr bdp serve` is, and it reads only: no page takes a write,
and every route refuses any method but GET and HEAD.

```sh
yardr web serve                          # http://127.0.0.1:8791/
yardr web serve --addr 127.0.0.1:9000
curl -s localhost:8791/                                  # the page
curl -s -H 'Accept: application/json' localhost:8791/    # the same data
```

Parts not built yet are marked **planned**, with the bead that builds them.

## Loopback only

There is no authentication yet, so the view listens on the loopback
interface and nowhere else:

- `--addr` takes `127.0.0.1`, `::1` or `localhost` with a port. Any other
  address, and one with no host (`:8791` listens on every interface), is
  refused before the yard is touched, with a message that says why.
- A request whose `Host` header is not a loopback name is answered 421. A
  page on another site can otherwise point a name of its own at 127.0.0.1
  and read the yard through the visitor's browser (DNS rebinding).

To look at a yard on another machine, forward the port over ssh
(`ssh -L 8791:127.0.0.1:8791 host`).

## No other host

The browser gets everything from this server: HTML rendered with
`html/template`, one stylesheet, and htmx with its server-sent events
extension, vendored under `internal/web/assets/` and embedded in the binary.
There is no CDN, no web font, no analytics, no Node and no build step;
`go install .` still gives one binary. `internal/web/assets/README.md` names
the version, source, licence and sha256 of each vendored file, and a test
holds the files to it.

Two things keep it so: a test fetches every page and asset and fails on a
URL that names another host, and every response carries
`Content-Security-Policy: default-src 'self'`, under which the browser
itself refuses to load or call anything else. The policy also refuses
inline `<script>`, inline `<style>` and `style=` attributes, so a page has
none: scripts and styles live in the asset files.

## Pages

| path | page | JSON |
|---|---|---|
| `/` | Home: the feed of what happened, newest first and live, under the yard's rigs with the open beads per stage of their flows | `Home` |
| `/?before=<seq>` | an older page of the feed: the 50 entries under that seq | `Home` |
| `/events` | the yard's events as they happen, as server-sent events ([The stream](#the-stream)) | `?format=json` |
| `/line` | the right column as a page, for a window too narrow for the column: what waits for a person, and the line in numbers ([The right column](#the-right-column)) | `Line` |
| `/sessions` | the running sessions, the oldest first, with bead, stage, group, runner, state and age (`yardr session list`), live | `Sessions` |
| `/residents` | each resident: whether it runs, since when, and its unread inbox count, live. Journals and handoffs are not shown | `Residents` |
| `/flows`, `/beads` | *planned, yardr-822r*: answers 501 with a "not built yet" page | `{"page": "beads", "built": false}` |
| `/assets/<file>` | the stylesheet and the two scripts | |

The feed, the right column, the sessions and the residents are live
([Live pages](#live-pages)). The rigs' stage counts above the feed are a
snapshot: reload the page to see them change.

Home as JSON:

```json
{
  "yard": "yardr",
  "rigs": [
    {
      "name": "yardr", "kind": "git",
      "open": 6, "working": 1, "held": 1,
      "flows": [
        {"name": "default", "stages": [{"stage": "backlog", "open": 4}, {"stage": "new", "open": 1}, {"stage": "review", "open": 1}]},
        {"name": "yardr.epic", "type": "epic", "stages": [{"stage": "open", "open": 0}]}
      ],
      "elsewhere": [{"stage": "inbox", "open": 1}]
    }
  ],
  "events": [
    {"seq": 1255, "at": "2026-10-03T23:40:12.129975Z", "kind": "closed", "actor": "yardr-refinery",
     "bead": "yardr-tzzc", "title": "web: yardr web serve", "text": "landed in merged at 70dabcd",
     "note": "refinery: merged 70dabcd", "commit": "70dabcd", "tone": "landed"},
    {"seq": 1254, "at": "2026-10-03T23:40:12.129659Z", "kind": "advanced", "actor": "yardr-refinery",
     "bead": "yardr-tzzc", "title": "web: yardr web serve", "text": "moved from approved to merged",
     "note": "refinery: merged 70dabcd"}
  ],
  "head": 1255,
  "older": 1204
}
```

- `yard` is the `name` setting, else the home's directory name, as for
  residents ([configuration.md](configuration.md)).
- `open` counts the rig's open beads, `working` those a session has
  claimed, `held` those on hold.
- `flows` are the flows that govern the rig (`yardr flow show <rig>`): its
  own first, then one per bead type that has its own (`type`). A bead is
  counted under its type's flow when there is one, else under the rig's.
  Terminal stages are left out: entering one closes the bead, so no open
  bead sits there.
- `elsewhere` counts open beads in a stage their flow lacks: a bead pinned
  to another flow, one a changed flow left behind, or every bead of a rig
  with no flow.
- `error`, on a rig, is why its flows could not be read (a checkout git
  cannot read). The rig's beads are then all in `elsewhere`, and the other
  rigs still show.

- `events` is the feed, newest first: at most 50 entries, one per event
  ([The feed](#the-feed) says what each field holds).
- `head` is the yard's newest seq when the page was read. The first page
  holds everything up to it, so `/events?after=<head>` continues the page
  with no gap and no duplicate.
- `older` is the seq to ask for the next page with (`/?before=<older>`);
  it is absent when nothing older is retained. `before` repeats the seq an
  older page was asked with.

`/sessions` as JSON:

```json
{
  "sessions": [
    {"id": "vkqehjw2rt151h7s0xy7", "bead": "yardr-68k3", "title": "web: what waits for a person",
     "epic": "yardr-e3rg", "stage": "new", "group": "yardr-builders", "runner": "herdr yardr-68k3-new",
     "state": "running", "blocked": true, "started_at": "2026-10-04T00:02:11Z", "age": "3h 4m"}
  ]
}
```

- `runner` is the runner that started the session and what it runs there,
  as `yardr session list` shows it: `herdr <agent>`, `exec pid <pid>`,
  `manual`.
- `state` is `starting` or `running`: an ended session is not listed.
- `blocked` is set while the session waits on a permission prompt or a
  question: the dispatcher's last word on it was `session_blocked`.
- `age` is how long ago it started, in its two largest units, when the
  page was read.

`/residents` as JSON:

```json
{
  "residents": [
    {"name": "yardmaster", "state": "running", "running": true, "enabled": true,
     "since": "2026-10-03T21:14:02Z", "up": "5h 52m", "agent": "herdr · claude · opus", "unread": 3},
    {"name": "night", "state": "stopped", "running": false, "enabled": false, "agent": "herdr · claude", "unread": 0}
  ]
}
```

- `state` is `stopped`, `running` or `failed` (the daemon gave up starting
  it); `enabled` is false for one the daemon was told not to keep up.
- `since` and `up` are there while it runs: when its session started.
- `unread` counts the events in its inbox it has not acknowledged
  (`yardr resident show`). The events themselves, its journal and its
  handoffs are its own and are on no page.
- `restart` is why a restart was asked for that has not happened yet.

### JSON or HTML

Every page answers both from one value. A request gets JSON when its
`Accept` header names `application/json` (or `application/*`) with a higher
weight than `text/html`; everything else gets the page, so a browser, plain
`curl` and `*/*` all get HTML. Responses carry `Vary: Accept`.

Errors keep the form: a 404, a 501 or a 500 is the error page inside the
shell for a browser and `{"error": "..."}` for a JSON client (an unbuilt
page: `{"page": ..., "built": false}`).

## The right column

The same on every page, and `/line` as a page of its own where the window
has no room for it. As JSON (`/line`):

```json
{
  "waits": [
    {"bead": "yardr-9c2k", "title": "pick the port", "rig": "yardr", "stage": "new",
     "since": "2026-10-04T00:31:40Z",
     "why": [{"kind": "held", "text": "held by yardr-9c2k-new", "note": "8791 or 8080? the brief names both"}]},
    {"bead": "yardr-41ma", "title": "name the command", "rig": "yardr", "stage": "decide",
     "since": "2026-10-03T23:58:07Z",
     "why": [{"kind": "manual", "text": "waits for decisions (mckean) in decide"}]}
  ],
  "open": 6, "working": 2, "held": 1, "sessions": 3,
  "stages": [{"stage": "backlog", "open": 3}, {"stage": "new", "open": 2}, {"stage": "decide", "open": 1}],
  "rigs": [{"name": "yardr", "open": 6}]
}
```

`waits` are the open beads no agent will move, each with every reason
(`why`) that holds for it:

| `kind` | the bead | `text` |
|---|---|---|
| `held` | is on hold | `held by <actor>`, and as `note` the reason: the note that actor left right before, as in [the feed](#the-feed) |
| `blocked` | has a running session that waits on a permission prompt or a question | `its session waits on a prompt: <agent>`, the agent's name in its runner, else the session's id |
| `stranded` | sits in a stage its flow lacks (`yardr rig check`) | `stranded in <stage>, a stage flow <flow> lacks` |
| `manual` | is in a stage of a manual group: the group that claimed it, else the one its stage is routed to | `waits for <group> (<members>) in <stage>` |

- The bead that changed last is first; `since` is that change, about when
  it began to wait.
- Whether a session is blocked is kept nowhere but in the events: the
  latest `session_blocked` or `session_unblocked` among the bead's last
  200 that names the session decides. A session that left its prompt while
  no daemon ran has no `session_unblocked`, and shows blocked until the
  session ends.
- When the yard cannot say which beads are stranded, the error is logged
  and the column shows the rest.

Under them, the line in numbers, summed over the rigs: `open` beads,
those a session has claimed (`working`), those on hold (`held`), the
`sessions` running (a manual group's among them), and the open beads per
stage (`stages`, in the order of the flows, a stage with none left out)
and per rig (`rigs`).

## Live pages

Every page opens one stream: the shell names it
(`sse-connect="/events?after=<head>"`), `head` being the yard's newest seq
from before the page's data was read, so the stream brings everything the
page lacks. Three things hang on it:

- the right column reads `/line` again on every event and puts the answer
  in its place (`hx-trigger="sse:message"`), and so does `/line`'s own copy
  of it. A bead that is held shows there with its reason as soon as the
  event arrives, and goes when it is unheld;
- `/sessions` and `/residents` read themselves again the same way;
- Home's feed takes each event's fragment on top ([The stream](#the-stream)).

While one such read is under way, htmx keeps only the last of the events
that arrive, so a burst costs two reads and not one each. What changes
with no event is as old as the last one: an age (`3h 4m`), and a
resident's unread count after it acknowledged its inbox, which the yard
does not log.

One stream per page, because a browser allows a host six connections
(HTTP/1.1) over all its tabs: a seventh page of the yard waits for one.

## The feed

Home shows what happened in the yard, one entry per event, the newest on
top. The first page holds the latest 50; the link under them (`Older`)
is the next page, and htmx follows it by itself when it scrolls into view,
so scrolling down goes back in time. Without scripts it is a plain link.

An entry says its event in the yard's words:

| event | `text` | `note` |
|---|---|---|
| `created` | created | |
| `advanced` | moved from *A* to *B*, with the outcome when it had one: `moved from new to review (done)` | the note the move came with |
| `closed`, on entering a terminal stage | landed in *stage*, with the commit: `landed in merged at 70dabcd` (`tone` `landed`, `commit`) | the note of the move that landed it |
| `closed`, by hand | closed: *reason* | |
| `held` | held (`tone` `held`) | the reason |
| `unheld`, `reopened`, `released` | no longer held; reopened; released, to be routed again | for a release, its reason |
| `noted` | noted | the note |
| `claimed` | claimed by *group* for *stage* | |
| `started` | session started, with the agent's name when the runner gives one | |
| `session_<state>` (a session the dispatcher ended) | session ended: *state* (`tone` `held`) | the reason |
| any other | the kind, spelled out: `setting changed` | its plain values: `key name · value north` |

- A resident's journal entries and handoffs (`journal`, `handoff`) are left
  out: they are its notes to itself.
- The reason of a hold and the note of a move are the note their actor
  left on the bead right before (`yardr bead note … && yardr bead hold`,
  `yardr bead advance --note`): the bead's previous event must be that
  actor's note. Anyone else's note, or an older one, is not shown as the
  reason.
- A landing's `commit` is the first commit hash in the note of the move
  that landed the bead, which is where the refinery writes it (`merged
  70dabcd`). The yard records a landing's commit nowhere else, so a merger
  that writes no such note gives a landing without one.
- `note` is cut at 280 bytes. `title` is the bead's title now, not when
  the event happened.
- The time on an entry is this machine's: the view is served on loopback,
  to a browser on the same machine.

## The stream

`GET /events` is the yard's event log as server-sent events: one per
event, in order, the event's `seq` as its `id`.

```sh
curl -N 'localhost:8791/events?format=json'            # from now on
curl -N 'localhost:8791/events?format=json&after=0'    # every retained event, then live
curl -N -H 'Last-Event-ID: 1255' localhost:8791/events # after 1255, as HTML fragments
```

**Where it starts.** After the seq in the `Last-Event-ID` header, else
after `?after=<seq>`, else from now. From there it carries every event
once, with no gap, as `yardr watch --after` does. A browser's
`EventSource` sends `Last-Event-ID` by itself when it reconnects, and the
header wins over the URL, which still says where the page first started.
So:

- stopping and starting `yardr web serve` under an open page loses and
  repeats nothing: the browser reconnects (the stream tells it to retry
  after half a second) and the new process goes on after the last event
  the page got, including what happened while none was up;
- a restart of the yardr server is ridden out inside the stream: the
  connection to the browser stays open, and the stream resubscribes after
  the last event it sent once the server is back.

**What it carries.**

- By default the `data` is the entry of [the feed](#the-feed) as an HTML
  fragment, the page's own markup (`templates/entry.html`). The shell
  names the stream (`sse-connect="/events?after=<head>"`) and htmx puts
  each fragment on top of Home's feed ([Live pages](#live-pages)). An event the feed leaves out is sent as its `id`
  with no `data`: no message for the page, but its cursor passes it.
- With `?format=json`, or an `Accept` that prefers JSON, the `data` is the
  event itself, as `yardr watch --json` prints it
  (`{"seq", "id", "bead", "kind", "actor", "data", "at"}`), and no event is
  left out. `EventSource` cannot set `Accept`; the parameter is for it.

```
id: 1254
data: {"seq":1254,"id":"1254","bead":"yardr-tzzc","kind":"advanced","actor":"yardr-refinery","data":{"from":"approved","to":"merged"},"at":"2026-10-03T23:40:12.129659Z"}
```

**Clients.** Each stream has its own subscription to the yard, so one
client never waits on another. A write to a client waits 10 seconds at
most: one that takes nothing for that long is dropped, and reconnects
where it stopped if it still lives. A quiet stream gets a comment line
every 20 seconds, which finds a client that went away without closing.

One case repeats entries in the page: htmx's extension opens a new
`EventSource` itself, with the page's first URL and no `Last-Event-ID`,
when the browser gives the old one up, which a browser does when a
reconnect is answered with anything but a stream (another program on the
port, for one). The entries since the page was loaded then come again.
Reload the page.

## Layout

One column, like x.com. Simplicity is the point: **a new view is a new feed
in the centre column, never a new arrangement.**

- **Left**, 200px: the yard's name and a short menu (Home, Flows, Beads,
  Sessions, Residents).
- **Centre**, up to 600px: the page. It is the only place content scrolls;
  the body itself never does.
- **Right**, 300px: what waits for a person, and a summary of the line. It
  is the same on every page.

| window | arrangement |
|---|---|
| 1100px and wider | menu, centre, right |
| 700px to 1099px | menu and centre; the right column goes, and a Line entry in the menu leads to `/line` |
| under 700px (a phone) | the centre column alone; the menu is a bar at the bottom, Line included |

The shell is `internal/web/templates/shell.html`; the right column is the
`aside` template (`aside.html`), which `/line` renders in the centre.

## Look

Neutral greys, square corners, monospace. The tokens are custom properties
at the top of `internal/web/assets/yard.css`, the one stylesheet. Use them;
add none without need, and never write a colour, a face or a column width
anywhere else.

| token | dark | light | for |
|---|---|---|---|
| `--ground` | `#17171a` | `#efece5` | the page |
| `--panel` | `#1e1e22` | `#e7e3da` | panels, the phone's menu bar |
| `--hairline` | `#26262b` | `#dcd7cb` | rules between entries, panel borders |
| `--hairline-strong` | `#35353d` | `#c6c0b2` | a rule that must show on a panel |
| `--ink` | `#eae8ee` | `#15140f` | text |
| `--dim` | `#cdccd2` | `#3b3930` | secondary text |
| `--faint` | `#b0afb6` | `#625f54` | labels, zeros, what is done |
| `--accent` | `#a9ceff` | `#3c5777` | the one accent: the current page, a link in use, focus |
| `--landed` | `#cfa548` | `#8d6830` | the second accent, for what landed and nothing else |
| `--working` | the accent | the accent | status: a session is working |
| `--running` | `#6db58a` | `#2f7d55` | status: idle or running |
| `--held` | `#e0735f` | `#b3402c` | status: blocked or held, and errors |
| `--done` | faint | faint | status: done |
| `--mono` | | | the main face: `"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, monospace` |
| `--sans` | | | longer prose: `"Inter", system-ui, sans-serif` |
| `--text`, `--small`, `--large` | | | 14px, 11px (labels), 20px (names and figures) |
| `--gap` | | | 16px, the one spacing unit |
| `--label-spacing` | | | 0.14em, for uppercase labels |
| `--menu-width`, `--centre-width`, `--right-width` | | | the three columns |

The light theme applies under `prefers-color-scheme: light`. The fonts are
the reader's own: none is downloaded, so a machine without JetBrains Mono
or Inter shows its system monospace and sans.

Rules the stylesheet holds to, and a test checks the second:

- Small uppercase labels with wide letter-spacing (`.label`) name things;
  values are in the plain face.
- Corners are square everywhere (`border-radius: 0`). The one circle is a
  status dot (`.dot`, with `.working`, `.running` or `.held`).
- Words come from the yard: line, landed, held, waits for you.

Classes a page builds from: `.item` (one entry of the centre feed, with a
hairline under it), `.item-head`, `.label`, `.panel`, `.figures` and
`.rows` (name and number pairs), `.stages` (a row of counted stages),
`.feed` and `.entry` (the feed and one event of it, with `.entry-head`,
`.entry-what`, `.entry-title`, `.entry-note`), `.wait` (a bead that waits,
with `.wait-title` and `.why`), `.bead` (a bead's handle), `.state` (a
status dot and its word),
`.num`, `.zero`, `.faint`, `.held`, `.landed`, `.dot`.

## Adding a page

A page is one template and one handler, in `internal/web`.

1. **The data.** A struct with `json` tags: it is the page's JSON, so name
   its fields for a client, not for the template.
2. **The template**, `templates/<name>.html`, defining `content`:

   ```html
   {{define "content"}}
   {{range .Data.Beads}}<article class="item">…</article>{{end}}
   {{end}}
   ```

   It is rendered inside the shell with `.Data` (your struct), `.Title` and
   `.Yard`. Every file in `templates/` is picked up; no list to edit.
3. **The handler** builds the data once and ends in `respond`, which picks
   JSON or HTML. It never looks at `Accept` itself, and hands an error to
   `fail` (404 for `store.ErrNotFound`, 500 otherwise):

   ```go
   func (s *Server) handleBeads(w http.ResponseWriter, r *http.Request) {
       beads, err := s.st.ListBeads(r.Context(), store.BeadFilter{})
       if err != nil {
           s.fail(w, r, err)
           return
       }
       s.respond(w, r, Page{Template: "beads", Title: "Beads", Menu: "beads", Data: Beads{Beads: beads}})
   }
   ```

   `Menu` is the menu entry to mark as current.

   A live page reads the yard's head before its data and gives it to the
   page (`head`), and its template wraps what changes in an element that
   reads the page again on every event (`templates/sessions.html`):

   ```html
   <div id="live" hx-get="/sessions" hx-trigger="sse:message" hx-select="#live > *" hx-swap="innerHTML ignoreTitle:true">
   ```
4. **The route**, in `routes` (`server.go`), with its method:
   `s.mux.HandleFunc("GET /beads", s.handleBeads)`. For a page the menu
   already links to, take its name out of `unbuilt`.
5. **Tests** in `web_test.go`, with `httptest`: the JSON and the HTML of the
   same request. `TestNoOtherHost` walks every link from Home, so a new
   page is checked for other hosts and inline styles once the menu or a
   page links to it; raise the page count it expects.
6. **This file**: the row in Pages, and the JSON's shape.

A template that is a part of a page and no page (`shell`, `aside`, `entry`)
is named in `partial` (`server.go`), and every page is parsed with it.

A page adds no script and no stylesheet. If it needs a new class, it goes
in `yard.css` and uses the tokens.
