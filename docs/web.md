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
| `/` | Home: the yard's name, its rigs, and per rig the open beads per stage of its flows | `Home` |
| `/line` | the right column as a page, for a window too narrow for the column | `Line` |
| `/flows`, `/beads` | *planned, yardr-822r*: answers 501 with a "not built yet" page | `{"page": "beads", "built": false}` |
| `/sessions`, `/residents` | *planned, yardr-68k3*: the same | the same |
| `/assets/<file>` | the stylesheet and the two scripts | |

The live feed on Home (server-sent events, no reload) is *planned,
yardr-ardv*; what waits for a person, in the right column, is *planned,
yardr-68k3*. Until then the page is a snapshot: reload it to see a change.

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
  ]
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

`/line` as JSON is `{"open", "working", "held", "rigs": [{"name", "open"}]}`,
summed over the rigs.

### JSON or HTML

Every page answers both from one value. A request gets JSON when its
`Accept` header names `application/json` (or `application/*`) with a higher
weight than `text/html`; everything else gets the page, so a browser, plain
`curl` and `*/*` all get HTML. Responses carry `Vary: Accept`.

Errors keep the form: a 404, a 501 or a 500 is the error page inside the
shell for a browser and `{"error": "..."}` for a JSON client (an unbuilt
page: `{"page": ..., "built": false}`).

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
4. **The route**, in `routes` (`server.go`), with its method:
   `s.mux.HandleFunc("GET /beads", s.handleBeads)`. For a page the menu
   already links to, take its name out of `unbuilt`.
5. **Tests** in `web_test.go`, with `httptest`: the JSON and the HTML of the
   same request. `TestNoOtherHost` walks every link from Home, so a new
   page is checked for other hosts and inline styles once the menu or a
   page links to it; raise the page count it expects.
6. **This file**: the row in Pages, and the JSON's shape.

A page adds no script and no stylesheet. If it needs a new class, it goes
in `yard.css` and uses the tokens.
