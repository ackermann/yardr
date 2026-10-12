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

## Loopback only

There is no authentication yet, so the view listens on the loopback
interface and nowhere else, unless `--unsafe` is given (below):

- `--addr` takes `127.0.0.1`, `::1` or `localhost` with a port. Any other
  address, and one with no host (`:8791` listens on every interface), is
  refused before the yard is touched, with a message that says why.
- A request whose `Host` header is not a loopback name is answered 421. A
  page on another site can otherwise point a name of its own at 127.0.0.1
  and read the yard through the visitor's browser (DNS rebinding).

To look at a yard on another machine, forward the port over ssh
(`ssh -L 8791:127.0.0.1:8791 host`).

### `--unsafe`: every interface

`yardr web serve --unsafe` listens on `0.0.0.0:8791`, every interface, so
that a phone on the same LAN can open `http://<this machine's IP>:8791/`.
There is still no authentication: anyone who can reach this machine on that
port can read the whole yard. The view stays read-only.

- With `--unsafe`, `--addr` takes any address: `:9000`, `0.0.0.0:9000`, or
  one LAN address (`192.168.178.90:9000`) to listen on that interface only.
- Without `--unsafe` nothing changes, and the refusal of an address off
  loopback names the flag.
- A request is answered when its `Host` header is an IP address (any), a
  loopback name, or this machine's host name as `os.Hostname` gives it,
  with or without `.local`, in any case (`adventuretime` and
  `adventuretime.local`), or a name given with `--host` (below). Every
  other name is still answered 421, so a page on another site cannot read
  the yard through a visitor's browser by pointing a name of its own at
  this machine.
- `--host <name>` names a host the machine is reached by and cannot work
  out by itself: its name in the LAN's DNS, on a tailnet, or a proxy's.
  It is answered as the machine's own names are, in any case and with any
  port. The flag takes one name or a comma list and may be repeated
  (`--host yard.example.net --host yard.lan`). It goes with `--unsafe`
  only: without it the view is on loopback, where no such name reaches
  it, and the flag is refused. A wildcard (`*`, `*.ts.net`) and an empty
  name are refused too: a view that answers to every name has no guard
  left.
- The 421 says which names are answered and, for a name that could be a
  host's, how to add it:
  `... not yard.example.net:8791. If that name is this machine's, start the view with --host yard.example.net`.
- The event streams one address may hold open, and all addresses
  together, are limited ([The stream](#the-stream), **How many**), so a
  device on the network cannot hold streams open without end. Loopback's
  are counted apart.
- It logs one warning when it starts:
  `no authentication: anyone who can reach this machine on port 8791 can read the yard`.
  The line after it says what the view answers to:
  `serving the yard url=http://[::]:8791/ answers_to="an IP address, localhost, adventuretime, adventuretime.local, yard.example.net"` when started with `--unsafe --host yard.example.net`.

## Keeping it running

`yardr web serve` runs while its terminal does. Two commands run it without
one, both on loopback only (`--addr` takes a loopback address with a port,
default `127.0.0.1:8791`; neither has `--unsafe`, and so neither has
`--host`):

```sh
yardr web start                  # in the background, until it is stopped or the machine restarts
yardr web stop                   # stop every view that runs on the yard
yardr web status                 # the views that run, and whether the login service is installed
yardr web install                # as a login service: now, and at every login
yardr web install --addr 127.0.0.1:9000
yardr web uninstall
```

`yardr web start` starts `yardr web serve` detached, logging to
`<home>/web.log`, waits until it listens and prints its address, its pid and
how to stop it. It refuses an address something listens on already.

`yardr web stop` stops the views that the records under `<home>/webviews/`
name ([below](#where-it-runs)), however each was started: in a terminal, by
`yardr web start`, by the login service. It signals each and waits
(`--wait`, default 30s) until the view has let go of its record, then names
each with its pid and address; with none running it says so. A `yardr web
start` after it answers at the same address. A view the login service ran
stays stopped until the next login, since it ended without a failure;
`yardr web uninstall` is what removes the service. The verbs are the same
for every long-running command
([configuration.md](configuration.md#long-running-commands)).

`yardr web install` writes the view as a login service of this user, a
launchd agent on macOS and a systemd user unit on Linux, and loads it: the
view is started now and at every login, and started again if it ends with a
failure. It prints the address. The file, its name, `--file-only` and
`yardr web uninstall` are as for the daemon
([configuration.md](configuration.md#login-services)). Stop a view started
with `yardr web start` before installing one on the same address: the
service's cannot listen while the other does, and is tried again every 10
seconds. The service keeps running the build it was started from; after an
upgrade, `yardr web install` again restarts it on the new one.

The onboarding offers both once the yard runs: "Start the web view now?"
and, on a yes, "And at login?", each starting at no.
[`yardr doctor`](doctor.md#optional) reports the view as configured once the
service is installed, with its address.

## Where it runs

`yardr status` lists every web view that runs on the yard, however it was
started: the URL to open, its pid, the build it runs, and who can open it.

```
web view   http://127.0.0.1:8791/     pid 4801  build abcdef123456  this machine only
           http://192.168.1.20:9000/  pid 4950  build abcdef123456  open to the network (--unsafe), no login
           login service: not installed
```

`yardr status --json | jq -r '.web.views[].url'` prints the URLs alone.
`yardr doctor` says the same in its `web view` line under Optional, and adds
a `note` for a view that is open to the network.

- A view that listens on every interface (`--unsafe` without `--addr`) has
  no address of its own, so its URL names this machine's first IPv4 address
  off loopback, else its host name. `addr` in the JSON is what it listens
  on (`0.0.0.0:8791`).
- `open to the network` is a view started with `--unsafe` on an address off
  loopback. `--unsafe --addr 127.0.0.1:9000` is `this machine only`.
- A view does not replace itself when a new yardr is installed, as the
  server and the daemon do: it runs the build it was started on until it is
  started again. When that is another build than the yardr that was asked,
  a line under the views says so (`this yardr is build <build>: ...`); stop
  the view and start it again to have the new one (`yardr web install`
  again, for the login service's).
- The views are read from the home, not from the server, so status lists
  them while the server is stopped too, and starts none.
- While it serves, a view keeps a record in `<home>/webviews/` (its
  address, its pid, `--unsafe` or not, when it started, the build it runs)
  and holds a lock on it. It removes the record when it stops. One that ended without doing so
  (`kill -9`, a reboot) holds the lock no longer, so its record is not
  listed, whatever its pid has become since; the next view to start removes
  it. Nothing is found by process name or by trying a port.
- A view started by a build of yardr from before `yardr status` keeps no
  record and is not listed until it is started again.

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
| `/` | Home: the overview — what waits for a person, what runs now, the depots, what closed today — above the feed of what happened, newest first and live | `Home` |
| `/?before=<seq>` | an older page of the feed: the 50 entries under that seq, without the overview | `Home` |
| `/events` | the yard's events as they happen, as server-sent events ([The stream](#the-stream)) | `?format=json` |
| `/line` | the right column as a page, for a window too narrow for the column: what waits for a person, and the line in numbers ([The right column](#the-right-column)) | `Line` |
| `/flows` | per depot, the flows that govern it: each stage a section with its group and the open beads in it as cards, which move as the beads do ([Flows](#flows)) | `Flows` |
| `/flows?panel=1` | the yard panel: every depot's flows drawn as track, with the open beads standing on them as blocks ([Flows](#flows)) | `Flows` |
| `/beads` | the open beads, the newest change first ([Beads](#beads)) | `Beads` |
| `/beads?depot=<depot>&stage=<stage>` | the same, of one depot, in one stage, or both | `Beads` |
| `/beads?all=1` | the closed beads too, every one (`yardr bead list --all`) | `Beads` |
| `/beads/<id>` | one bead: what `yardr prime --bead <id>` knows, for a person ([One bead](#one-bead)) | `BeadPage` |
| `/sessions` | the running sessions, the oldest first, with bead, stage, group, runner, state and age (`yardr session list`), and under them the claims of manual groups, live | `Sessions` |
| `/sessions?all=1` | the same page; its JSON lists the ended sessions too, every one (`yardr session list -a --all`) | `Sessions` |
| `/crew` | each crew member: whether it runs, since when, and its unread inbox count when there is one, live. Journals and handoffs are not shown | `Crew` |
| `/depots`, `/groups`, `/routes`, `/peers`, `/deps`, `/limits` | no page: the yard's depots, groups, routes, peers and edges as JSON, and the quota ([The lists](#the-lists)) | `Depots`, `Groups`, `Routes`, `Peers`, `Deps`, `Limits` |
| `/assets/<file>` | the stylesheet and the two scripts | |

The feed, the right column, `/flows`, `/beads`, a bead's page, the sessions
and the crew are live ([Live pages](#live-pages)). The overview above the
feed is a snapshot: reload the page to see it change.

Home as JSON:

```json
{
  "yard": "yardr",
  "depots": [
    {
      "name": "yardr", "kind": "git",
      "open": 6, "working": 1, "held": 1,
      "flows": [
        {"name": "default", "stages": [{"stage": "backlog", "open": 4}, {"stage": "new", "open": 1}, {"stage": "review", "open": 1}]},
        {"name": "yardr.train", "type": "train", "stages": [{"stage": "open", "open": 0}]}
      ],
      "elsewhere": [{"stage": "inbox", "open": 1}]
    }
  ],
  "attention": [
    {"bead": "yardr-9c2k", "title": "pick the port", "depot": "yardr", "stage": "new",
     "since": "2026-10-04T00:31:40Z",
     "why": [{"kind": "held", "text": "held by yardr-9c2k-new", "note": "8791 or 8080?"}]}
  ],
  "running": [
    {"id": "vkqehjw2rt151h7s0xy7", "bead": "yardr-68k3", "title": "web: what waits for a person",
     "stage": "new", "group": "yardr-builders", "runner": "herdr yardr-68k3-new",
     "state": "running", "started_at": "2026-10-04T00:02:11Z", "age": "3h 4m"}
  ],
  "closed": [
    {"id": "yardr-tzzc", "title": "web: yardr web serve", "closed_at": "2026-10-04T00:31:40Z"}
  ],
  "events": [
    {"seq": 1255, "at": "2026-10-03T23:40:12.129975Z", "kind": "closed", "actor": "yardr-refinery",
     "bead": "yardr-tzzc", "title": "web: yardr web serve", "text": "landed in merged at 70dabcd",
     "note": "assembly: merged 70dabcd", "commit": "70dabcd", "tone": "landed"},
    {"seq": 1254, "at": "2026-10-03T23:40:12.129659Z", "kind": "advanced", "actor": "yardr-refinery",
     "bead": "yardr-tzzc", "title": "web: yardr web serve", "text": "moved from approved to merged",
     "note": "assembly: merged 70dabcd"}
  ],
  "head": 1255,
  "older": 1204
}
```

- `yard` is the `name` setting, else the home's directory name, as for
  crew members ([configuration.md](configuration.md)).
- `open` counts the depot's open beads, `working` those an agent's or a
  command's session has claimed, `held` those on hold. A bead a manual
  group has claimed is not `working`: it is a person's turn
  ([Manual claims](#manual-claims)).
- `flows` are the flows that govern the depot (`yardr flow show <depot>`): its
  own first, then one per bead type that has its own (`type`). A bead is
  counted under its type's flow when there is one, else under the depot's.
  Terminal stages are left out: entering one closes the bead, so no open
  bead sits there.
- `elsewhere` counts open beads in a stage their flow lacks: a bead pinned
  to another flow, one a changed flow left behind, or every bead of a depot
  with no flow.
- `error`, on a depot, is why its flows could not be read (a checkout git
  cannot read). The depot's beads are then all in `elsewhere`, and the other
  depots still show.
- On the page a depot is one line with its open count (`nothing open` when
  there is none), and under it each flow that holds a bead as stage count
  pairs on one line (`backlog 4 · new 1`); a stage with no bead is not
  shown, and neither is a flow with none. The page prints no zero.

- `attention` are the open beads that wait for a person, one line each with
  a link: everything something stops (held, blocked on a prompt, stranded)
  and the parked beads past their flow's first, in the line's order, faults
  first. A bead parked where it starts (a backlog) is on `/line` only.
- `running` are the sessions running now, the oldest first: the rows of
  `/sessions` without the claims of manual groups, which run nothing.
- `closed` are the beads that closed today, the newest first, at most ten.

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
     "train": "yardr-e3rg", "stage": "new", "group": "yardr-builders", "runner": "herdr yardr-68k3-new",
     "state": "running", "blocked": true, "started_at": "2026-10-04T00:02:11Z", "age": "3h 4m"}
  ],
  "manual": [
    {"id": "t0m3xq8c4k2v9d5h1w7a", "bead": "yardr-41ma", "title": "name the command",
     "stage": "decide", "group": "decisions", "runner": "manual",
     "state": "claimed", "started_at": "2026-10-03T23:58:07Z", "age": "3h 8m"}
  ],
  "list": [
    {"id": "vkqehjw2rt151h7s0xy7", "bead": "yardr-68k3", "group": "yardr-builders", "stage": "new",
     "runner": "herdr", "kind": "claude", "model": "opus", "state": "running", "handle": {"agent": "yardr-68k3-new"}, "stopped": false,
     "started_at": "2026-10-04T00:02:11Z", "train": "yardr-e3rg"},
    {"id": "t0m3xq8c4k2v9d5h1w7a", "bead": "yardr-41ma", "group": "decisions", "stage": "decide",
     "runner": "manual", "state": "running", "handle": {"manual": true}, "stopped": false,
     "started_at": "2026-10-03T23:58:07Z"}
  ],
  "head": 1301
}
```

- `sessions` are the sessions an agent or a command runs. `manual` are the
  claims of manual groups, which `yardr session list` has among the
  sessions: the page lists them under the running ones, headed "Claimed by
  a manual group" with their count, with no status dot and no runner, which
  is always `manual`, as the heading says
  ([Manual claims](#manual-claims)). "No session is running." is about
  `sessions` alone.
- `runner` is the runner that started the session and what it runs there,
  as `yardr session list` shows it: `herdr <agent>`, `exec pid <pid>`,
  `manual`.
- `state` is `starting` or `running`: an ended session is not listed. A
  manual claim's is `claimed`: the store keeps it as a running session,
  and nothing runs.
- `blocked` is set while the session waits on a permission prompt or a
  question: the dispatcher's last word on it was `session_blocked`.
- `age` is how long ago it started, in its two largest units, when the
  page was read.
- `list` are the same sessions as `yardr session list --json` prints them,
  field for field ([The lists](#the-lists)), the claims of manual groups
  among them, with `reset_wait` as that command has it. With
  `?all=1` the ended sessions are in it too, every one the yard keeps
  (`session list -a --all`), with `state` (`done`, `released`, `held`,
  `died`, `failed`, `aborted`), `ended_at` and `error`. It is beside
  `sessions` and `manual`, not in their place, because those rows say
  `runner` and `state` as the page does (`herdr <agent>`, `claimed`) and
  the command says them as the store has them (`herdr`, `running`). The
  page itself shows the live sessions either way. `kind` and `model` are
  the harness kind and model the session was started with, recorded at the
  claim; a session from before they were recorded, and one of a group that
  names none (manual, exec, peer), has neither.
- `head` is the yard's newest seq from before the sessions were read.

`/crew` as JSON:

```json
{
  "crew": [
    {"name": "yardmaster", "state": "running", "running": true, "enabled": true,
     "status": "idle", "since": "2026-10-03T21:14:02Z", "up": "5h 52m",
     "agent": "herdr · claude · opus", "kind": "claude", "unread": 3},
    {"name": "night", "state": "stopped", "running": false, "enabled": false,
     "agent": "herdr · claude", "kind": "claude", "unread": 0}
  ],
  "head": 1301
}
```

- `state` is `stopped`, `running` or `failed` (the daemon gave up starting
  it); `enabled` is false for one the daemon was told not to keep up.
- `status` is what its running session is doing, as the daemon last saw
  it: `working`, `idle`, `blocked` or `unknown`; absent when none runs.
- `since` and `up` are there while it runs: when its session started.
- `agent` is its runner, kind and model in one string, for the page;
  `kind` is the kind alone (`config.kind` of `yardr crew list --json`).
- `unread` counts the events in its inbox it has not acknowledged
  (`yardr crew show`). The events themselves, its journal and its
  handoffs are its own and are on no page. On the page the count shows
  only when it is not zero.
- `restart` is why a restart was asked for that has not happened yet.
- `head` is the yard's newest seq from before the crew was read.

### The lists

The view's JSON is the yard's read API: a second front end is one more
client of it and of [the stream](#the-stream), and runs no command. So the
rule is: **whatever a list command prints with `--json`, the view answers
at a route of the same name, with the same fields.** The rows are the
command's own, of the same Go types, so a field a command gains is in the
view's answer too. They are under a key named as the route, beside `head`,
the yard's newest seq from before the list was read: `/events?after=<head>`
then brings whatever changed since.

| command | route | rows under |
|---|---|---|
| `yardr depot list` | `/depots` | `depots` |
| `yardr group list` | `/groups` | `groups` |
| `yardr route list` | `/routes` | `routes`, in match order |
| `yardr peer list` | `/peers` | `peers` |
| `yardr dep list <bead>`, for every bead | `/deps` | `deps`: every edge of the yard once, the oldest first, the ones of closed beads too |
| `yardr bead list` | `/beads` | `beads` |
| `yardr bead list --all` | `/beads?all=1` | `beads` |
| `yardr session list` | `/sessions` | `list` |
| `yardr session list -a --all` | `/sessions?all=1` | `list` |
| `yardr crew list` | `/crew` | `crew`, in part |
| `yardr bead history <bead>` | `/beads/<bead>` | `history` |
| `yardr flow show <depot>` | `/flows` | `depots[].flows`, as the page has them ([Flows](#flows)) |
| `yardr limits` | `/limits` | `limits` with `unmapped`: what the command answers with no kind named, one entry per kind in use a source answers for |

```json
{
  "groups": [
    {"name": "yardr-builders", "runner": "herdr", "config": {"kind": "claude", "model": "opus"}, "limit": 3},
    {"name": "decisions", "runner": "manual", "config": {}, "limit": 1, "members": ["mckean"]}
  ],
  "head": 1301
}
```

Where the route was a page before it was a list, the page's fields stand
and the command's are added:

- A bead of `/beads` has every field of `bead list --json` (`labels`,
  `priority`, `created_at`, `hold`, `body` and the rest) and the card's
  `held` and `working` beside them. The order is the page's, the newest
  change first, not the command's.
- `/sessions` has the command's rows under `list`, beside its own.
- `/crew` has every field of `crew list --json`, and the page's `running`,
  `since`, `up`, `agent`, `kind`, `unread` and `restart` beside them. The
  page's fields keep their names and meanings.

`/depots`, `/groups`, `/routes`, `/peers`, `/deps` and `/limits` have no
page: they answer JSON whatever the request accepts. `?all=1` is a list
command's `--all`: every closed bead, every ended session the yard keeps,
with no limit. Any value but none, `0` or `false` asks for it.

`/limits` is the one route a GET may run the operator's command for. Each
entry is the command's: the kind, the provider's report under `limits`, the
used-up window under `window` and `resets_at`, or the reason under
`limits_unknown`, with `unmapped` naming the kinds in use no source answers
for. A read asks the source at most once a minute per kind, however often
the route is read: reads in between share the last answer, and concurrent
reads the one run on its way. Each run is bounded by the 30-second Timeout,
as the command's is, and a provider that fails nulls its entry without
failing the route. The answer is the same on loopback and with `--unsafe`.

The table is where the rule holds today. The other list commands (`timer`,
`hook`, `source`, `user`, `link`, `pack`, `thread`, `review` and `flow
list`) have no route yet, and neither has `yardr events -n <N>`: the
log is [the stream](#the-stream), which replays what is retained from
`?after=<seq>`. A new list command gets its route with it, and an old one
when a client needs it: a handler in `lists.go` that ends in `listed`, the
route in `routes`, a row here.

### JSON or HTML

Every page answers both from one value (the lists with no page answer JSON
alone: [The lists](#the-lists)). A request gets JSON when its
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
    {"bead": "yardr-9c2k", "title": "pick the port", "depot": "yardr", "stage": "new",
     "since": "2026-10-04T00:31:40Z",
     "why": [{"kind": "held", "text": "held by yardr-9c2k-new", "note": "8791 or 8080? the brief names both"}]}
  ],
  "parked": [
    {"bead": "yardr-41ma", "title": "name the command", "depot": "yardr", "stage": "decide",
     "since": "2026-10-03T23:58:07Z",
     "why": [{"kind": "manual", "text": "waits for decisions (mckean) in decide"}]}
  ],
  "open": 6, "working": 2, "held": 1, "sessions": 2,
  "stages": [{"stage": "backlog", "open": 3}, {"stage": "new", "open": 2}, {"stage": "decide", "open": 1}],
  "depots": [{"name": "yardr", "open": 6}]
}
```

`waits` and `parked` are the open beads no agent will move, each with
every reason (`why`) that holds for it:

| `kind` | the bead | `text` |
|---|---|---|
| `held` | is on hold | `held by <actor>`, and as `note` the reason: the note that actor left right before, as in [the feed](#the-feed) |
| `blocked` | has a running session that waits on a permission prompt or a question | `its session waits on a prompt: <agent>`, the agent's name in its runner, else the session's id |
| `stranded` | sits in a stage its flow lacks (`yardr flow show <depot>`) | `stranded in <stage>, a stage flow <flow> lacks` |
| `manual` | is in a stage of a manual group: the group that claimed it, else the one its stage is routed to | `waits for <group> (<members>) in <stage>` |

- A bead whose only reason is `manual` is in `parked`, every other one in
  `waits`: nothing stops a parked bead, it sits in a stage that is a
  person's by design (a backlog, a decision). The column lists the parked
  under what waits, in a part headed "Parked" with their count; "Nothing
  waits for you." shows when both are empty. A parked bead that is also
  held or stranded is in `waits`, with its `manual` reason too.
- In both, the bead that changed last is first; `since` is that change,
  about when it began to wait.
- Whether a session is blocked is kept nowhere but in the events: the
  latest `session_blocked` or `session_unblocked` among the bead's last
  200 that names the session decides. A session that left its prompt while
  no daemon ran gets its `session_unblocked` on the next daemon's first
  pass, and shows blocked until then.
- When the yard cannot say which beads are stranded, the error is logged
  and the column shows the rest.

Under them, the line in numbers, summed over the depots: `open` beads,
those an agent's or a command's session has claimed (`working`), those on
hold (`held`), the `sessions` running, and the open beads per stage
(`stages`, in the order of the flows, a stage with none left out) and per
depot (`depots`). On the page only what is not zero is shown, and `Nothing
is open.` stands in their place when nothing is; `Open per depot` names
only the depots with an open bead, as `Open per stage` already did.

### Manual claims

A manual group runs nothing: the dispatcher claims a bead for it, and the
store keeps the claim as a running session with the runner `manual` until
a person moves the bead on. To the view that is no work in progress:

- it does not count in `working` (Home, `/line`) or in `sessions`
  (`/line`);
- `/sessions` lists it under `manual`, apart from the running sessions,
  with the state `claimed`;
- its bead is `parked` in the column, as is an unclaimed bead in a stage
  routed to a manual group, unless something else stops it.

A session is manual when its runner is `manual`; one from before sessions
recorded their runner, when its handle says so.

## Live pages

Every page opens one stream: the shell names it
(`sse-connect="/events?after=<head>"`), `head` being the yard's newest seq
from before the page's data was read, so the stream brings everything the
page lacks. A page that needs more of it than the feed's entries says so
(`Page.stream`) and the shell adds `&format=`: `card` on `/flows`, `seq` on
`/beads` and a bead's page ([The stream](#the-stream)). Whatever the
format, an event is one unnamed message, which is all the right column
listens for. These hang on the stream:

- the right column reads `/line` again on every event and puts the answer
  in its place (`hx-trigger="sse:message"`), and so does `/line`'s own copy
  of it. A bead that is held shows there with its reason as soon as the
  event arrives, and goes when it is unheld;
- `/sessions`, `/crew`, `/beads` and a bead's page read themselves
  again the same way;
- on `/flows` each card and each section listens for the message named
  after it ([Flows](#flows));
- the yard panel (`/flows?panel=1`) reads itself again on every event, as
  `/line` does ([The yard panel](#the-yard-panel));
- Home's feed takes each event's fragment on top ([The stream](#the-stream)).

While one such read is under way, htmx keeps only the last of the events
that arrive, so a burst costs two reads and not one each. What changes
with no event is as old as the last one: an age (`3h 4m`), and a
crew member's unread count after it acknowledged its inbox, which the yard
does not log.

One stream per page, because a browser allows a host six connections
(HTTP/1.1) over all its tabs: a seventh page of the yard waits for one.
The server limits them too, far above that ([The stream](#the-stream)).

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
| `updated` | updated | what changed, a field after the other: `title "a door" → "the door" · priority 2 → 1 · labels +wood · body`. The body is named alone: its diff is on the bead's page |
| `claimed` | claimed by *group* for *stage* | |
| `started` | session started, with the agent's name when the runner gives one | |
| `session_<state>` (a session the dispatcher ended) | session ended: *state* (`tone` `held`) | the reason |
| any other | the kind, spelled out: `setting changed` | its plain values: `key name · value north` |

- A crew member's journal entries and handoffs (`journal`, `handoff`) are left
  out: they are its notes to itself.
- The reason of a hold and the note of a move are the note their actor
  left on the bead right before (`yardr bead note … && yardr bead hold`,
  `yardr bead advance --note`): the bead's previous event must be that
  actor's note. Anyone else's note, or an older one, is not shown as the
  reason.
- A landing's `commit` is the first commit hash in the note of the move
  that landed the bead, which is where the assembly writes it (`merged
  70dabcd`). The yard records a landing's commit nowhere else, so a merger
  that writes no such note gives a landing without one.
- `note` is cut at 280 bytes. `title` is the bead's title now, not when
  the event happened.
- The time on an entry is this machine's: the view is served on loopback,
  to a browser on the same machine (under `--unsafe`, a phone elsewhere
  reads this machine's time too).

## Flows

`/flows` shows, per depot, each flow that governs it, as `yardr flow show
<depot>` does: the depot's own first, then one per bead type that has its own.
A flow is one column: its stages in the flow's order, each a section with

- the group a bead there is routed to (`by hand` with its members for a
  manual group; `no route: waits for a person` when no route matches),
- `stage file` when the stage has one,
- the open beads in it as cards, the newest change first, and their count.

A terminal stage is there too, to show where the flow ends; it holds a bead
only when one was reopened. Under a depot's flows, `off the flow` holds the
open beads in no stage of them: a bead that pins a flow of its own
(`--meta flow=`), one a changed flow left behind, and every bead of a depot
with no flow. It shows only while there is one.

```json
{
  "depots": [
    {
      "name": "yardr", "kind": "git",
      "flows": [
        {
          "name": "default", "source": "repo@0f415ee",
          "stages": [
            {"stage": "new", "group": "yardr-builders", "stage_file": ".yardr/flows/default/new.md",
             "next": ["review", "decide", "backlog"],
             "beads": [{"id": "yardr-822r", "title": "web: flows and beads, live", "type": "task", "stage": "new",
                        "status": "open", "depot": "yardr", "working": true, "group": "yardr-builders",
                        "train": "yardr-e3rg", "updated_at": "2026-10-04T00:09:51.2Z"}]},
            {"stage": "decide", "group": "decisions", "human": true, "members": ["mckean"], "next": ["new"], "beads": []},
            {"stage": "merged", "terminal": true, "next": [], "beads": []}
          ]
        },
        {"name": "yardr.train", "type": "train", "source": "store, type train, from repo@0f415ee", "stages": []}
      ],
      "elsewhere": []
    }
  ],
  "head": 1301
}
```

- A card (`beads`, `elsewhere`, and every list of beads on the other pages)
  is `{"id", "title", "type", "stage", "status", "depot", "held", "working",
  "group", "train", "updated_at"}`: `held` is a bead on hold, `working` one a
  session has claimed.
- `source` is where the flow comes from, in `yardr flow show`'s words.
- `error`, on a depot, is why its flows could not be read; its beads are then
  all in `elsewhere`, and the other depots still show.
- `head` is the yard's newest seq when the page was read, read before the
  beads: `/events?after=<head>&format=card` continues the page.

**A bead that moves** leaves its section and enters the other without a
reload. The page's stream is the shell's, with `format=card`
([The stream](#the-stream)): at each event of a bead its card takes itself
out of wherever the page has it, and the section the bead stands in now
puts the card on top, which is where a reload would show it (the newest
change first). A card that arrives because its bead was created, moved or
reopened is seen arriving (`.card.arrived`, an animation of the
stylesheet); a hold or a claim changes the card where it is. A bead that
closes leaves the page. The count beside a stage's name is the
stylesheet's, a CSS counter over the cards in the section, so it follows
them with no message of its own.

A section puts a card in with `hx-swap="afterbegin settle:0ms"`. Without
`settle:0ms` htmx waits 20ms before the new card listens, and a second
event of the bead in that time (a move, then the claim that follows it)
would not take it out: the page would show the bead twice.

A change to a flow itself (a new stage, another group) is no event of a
bead: reload the page to see it.

## The yard panel

`/flows?panel=1` (the Yard entry of the menu) draws every depot's flows as
a signal panel: one diagram per flow, the track as `yardr flow show <depot>`
reads it — the stages in order, the sidings above for the stages only a
person moves a bead out of, the other edges as lines below — with the open
beads standing on it as blocks. A flow with no open bead still draws its
bare track. The depots stand stacked, each diagram under its depot's name,
and the page scrolls vertically; each diagram pans horizontally at full
size.

How to read it:

- A block is its bead: tapping it opens the bead. Blocks stand newest
  first; past ten on one platform the rest fold into `+N`, and the count
  beside the stage name always says the true number. A stage with none
  carries no count. A train's children stand together with their train's
  short id faint once.
- A section is lit green while a session runs in it, and a running block
  carries the short name of the group that works it. Quiet blocks carry no
  group: no one works them.
- Every blocked kind is told apart: solid red for held, hollow red for
  gave up, hollow green for stalled, green with a red ring for blocked on
  a prompt, blue for waiting on a dependency with the first open blocker's
  short id after it (tapping the id opens the bead holding), a red diamond
  before a bead back on `failed` or `changes`.
- A stage that waits for a person carries a lamp: red where the wait is a
  fault (held, blocked, gave up), blue where it is a person's turn by
  design (a manual stage). A bead back on `failed` or `changes` lights no
  lamp: its diamond is its only mark. A stage no route serves is hollow
  with the faint words `no route`.
- Tapping a platform opens the stage on `/flows`, at its section.
- Under a depot's diagrams, `off the line: N` holds the open beads in no
  stage of its flows, each with its stage named: a bead stranded in a
  stage its flow lacks stands there, since no platform can hold it.

The whole panel reads itself again on every event of the stream
([Live pages](#live-pages)): a bead that moves is on its new platform
without a reload. The parameter shapes the HTML alone: as JSON the page
answers the same with it as without it. Every colour the panel uses is a
named token of the stylesheet (`--panel-*` for the track, with `--running`,
`--held` and `--accent` for lit green, fault red and person's-turn blue),
so the palette changes in one edit.

## Beads

`/beads` lists the open beads, the newest change first. `?depot=` keeps one
depot's, `?stage=` one stage's; the page offers both as links, each keeping
the other. A depot or stage nobody has gives an empty list, not an error.
A filter with nothing to choose is left out.
`?all=1` lists the closed beads too, every one, as `yardr bead list --all`
(`"all": true` in the JSON); the filters keep it.

```json
{
  "depot": "yardr", "stage": "new",
  "beads": [{"id": "yardr-822r", "uuid": "0199ab3c-5a1e-7c41-9d0e-2f6b8a417c55",
             "title": "web: flows and beads, live", "body": "…", "type": "task", "stage": "new",
             "status": "open", "priority": 2, "depot": "yardr", "labels": [], "group": "yardr-builders",
             "session": "vkqehjw2rt151h7s0xy7", "created_at": "2026-10-03T22:41:07.5Z",
             "updated_at": "2026-10-04T00:09:51.2Z", "created_by": "yardmaster", "revision": "7f3a9c12",
             "train": "yardr-e3rg", "working": true}],
  "depots": ["yardr"],
  "stages": ["backlog", "new", "review"],
  "head": 1301
}
```

A bead has the fields `yardr bead list --json` prints
([The lists](#the-lists)), and two of the card's: `working`, a bead an
agent's or a command's session has claimed
([Manual claims](#manual-claims)), and `held`, the bead's `hold` by the
name the cards of the other pages have.

`depots` and `stages` are what the filters can be set to: the yard's depots,
and the stages that hold a listed bead (of `depot`, when it is set). Journals
and memories are not work and not listed, as in `yardr bead list`.

The page is live by reading itself again: its stream is the shell's, with
`format=seq`, and it asks for its own URL at each event, one request at a
time.

## One bead

`/beads/<id>` is what `yardr prime --bead <id>` knows, for a person: title,
type, stage, status and priority; its depot, the flow that governs it and
where that comes from, its base, its train, the bead it was discovered from;
the open beads that block it; a train's children; the beads it led to; the
body; the stage file of its stage (without its
`summary:` line, in the page; whole in the JSON); the outcomes the
stage offers, each with the command that reports it (the next stages, for a
stage with no outcome); its edges; its notes, the oldest first; the
sessions that worked it; its history. A closed bead has a page too. An
unknown id is a 404.

```json
{
  "bead": {"id": "yardr-822r", "title": "web: flows and beads, live", "body": "Child 3 of train …", "type": "task",
           "stage": "new", "status": "open", "priority": 2, "depot": "yardr", "labels": [], "group": "yardr-builders",
           "session": "vf0dsef5y990pzq0ybnh", "created_at": "…", "updated_at": "…", "revision": "…"},
  "flow": {"name": "default", "source": "repo@0f415ee"},
  "base": "yard/yardr-e3rg",
  "train": {"id": "yardr-e3rg", "title": "Web view of the yard: live and read-only", "type": "train", "stage": "open", "status": "open"},
  "blockers": [],
  "deps": [{"from": "yardr-e3rg", "to": "yardr-822r", "kind": "parent", "created_at": "…"}],
  "stage_file": {"path": ".yardr/flows/default/new.md", "text": "A builder implements the bead here…"},
  "outcomes": [{"name": "done", "to": "review", "command": "yardr bead done yardr-822r"},
               {"name": "question", "to": "decide", "command": "yardr bead done yardr-822r --outcome question"}],
  "next": ["review", "decide", "backlog"],
  "notes": [{"bead": "yardr-822r", "author": "yardr-822r-new", "text": "…", "at": "…", "seq": 412}],
  "sessions": [{"id": "vf0dsef5y990pzq0ybnh", "bead": "yardr-822r", "group": "yardr-builders", "stage": "new",
                "runner": "herdr", "state": "running", "stopped": false, "started_at": "…"}],
  "history": [{"seq": 1188, "at": "…", "kind": "created", "actor": "yardmaster", "bead": "yardr-822r",
               "title": "web: flows and beads, live", "text": "created"},
              {"seq": 1290, "at": "…", "kind": "updated", "actor": "yardmaster", "bead": "yardr-822r",
               "title": "web: flows and beads, live", "text": "updated", "note": "priority 3 → 2 · body",
               "diff": "--- was\n+++ is\n@@ -1,1 +1,2 @@\n Child 3 of train …\n+Live over the stream.\n"}],
  "head": 1301
}
```

- `bead`, `deps`, `notes`, `sessions` and `stage_file` are the store's own
  records, as `yardr bead show --json`, `yardr prime --json` and `yardr
  session list --json` print them. `train`, `children` and `blockers` are
  cards, and so are `discovered_from` and `led_to`, present when the bead
  has such an edge (`bead create --discovered-from`).
- `flow` is absent for a bead no flow governs: it may move to any stage.
  `terminal` names those of `next` that close the bead.
- `history` is every event of the bead the log still has, the oldest first,
  as `yardr bead history <id> --json` prints it: an entry of
  [the feed](#the-feed), none left out, and under an update that changed
  the body, `diff`: the body before and after as a unified diff by line. An
  update logged before events kept the previous values has no `diff`. The
  history starts where pruning left the log
  ([configuration.md](configuration.md#retention-keeping-yarddb-bounded)):
  nothing else keeps what a bead was.
- `warnings` say what could not be read (a broken flow, an ambiguous
  train); the rest of the page still stands.

**Live.** The page reads itself again at each event of the yard (the
shell's stream, with `format=seq`), so a note added on the command line
shows without a reload, and so does a blocker that closes or a child that
moves.

**Untrusted text.** A bead's title, body and notes and a stage file are
text someone, or some agent, wrote. Every page writes them as text through
`html/template`, in a `<pre class="text">` that keeps their lines; nothing
renders them as markdown or HTML, so a `<script>` in a note is shown, not
run. A body or a note wraps at the column's width. A stage file (`<pre
class="text file">`) is wrapped by hand already, so it keeps its lines as
written, in the small size, and scrolls sideways inside its block where
the column is narrower than they are; the page itself never does. `TestBeadEscapes` feeds one through the title, the body, a note, a
note's author and a stage file, on every page and in the card stream.

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

**What it carries.** `?format=` chooses; the feed's entries without it.

- By default the `data` is the entry of [the feed](#the-feed) as an HTML
  fragment, the page's own markup (`templates/entry.html`). The shell
  names the stream (`sse-connect="/events?after=<head>"`) and htmx puts
  each fragment on top of Home's feed ([Live pages](#live-pages)). An
  event the feed leaves out is sent as its `id` with no `data`: no message
  for the page, but its cursor passes it.
- With `?format=json`, or an `Accept` that prefers JSON, the `data` is the
  event itself, as `yardr watch --json` prints it
  (`{"seq", "id", "bead", "kind", "actor", "data", "at"}`), and no event is
  left out. `EventSource` cannot set `Accept`; the parameter is for it.
- With `?format=seq` the `data` is the event's seq and nothing else, for a
  page that reads itself again when something happens (`/beads`, a bead's
  page). No event is left out.
- With `?format=card` every event is what `format=seq` sends, and an event
  of a bead first moves the bead's card on `/flows`, in two messages named
  after elements of the page, which listen for them (`sse-swap`): first
  `event: bead/<id>`, at which the card the page has takes itself out,
  then `event: at/<depot>/<type>/<stage>` (the section of that stage in the
  depot's flow for that bead type, the type empty for the depot's own flow;
  `off/<depot>` for a bead off the flow), whose `data` is the card as the
  bead is now. A bead that closed gets the first alone. Only the last
  message, the unnamed one with the seq, carries the `id`, so a browser cut
  off between them is sent the whole move again. A note changes no card
  and is sent as its seq alone. The names are the elements' ids, each part
  escaped as a URL path segment.

```
id: 1254
data: {"seq":1254,"id":"1254","bead":"yardr-tzzc","kind":"advanced","actor":"yardr-refinery","data":{"from":"approved","to":"merged"},"at":"2026-10-03T23:40:12.129659Z"}
```

```
event: bead/yardr-822r
data: left

event: at/yardr//review
data: <article class="card arrived" id="bead/yardr-822r" sse-swap="bead/yardr-822r" hx-swap="delete">
data: …

id: 1302
data: 1302
```

**Clients.** Each stream has its own subscription to the yard, so one
client never waits on another. A write to a client waits 10 seconds at
most: one that takes nothing for that long is dropped, and reconnects
where it stopped if it still lives. A quiet stream gets a comment line
every 20 seconds, which finds a client that went away without closing.

**How many.** The open streams are limited, since each holds a
subscription of the yard and the view does not know whose it is:

| | limit | past it |
|---|---|---|
| one address off loopback | 16 | `429 Too Many Requests` |
| all addresses off loopback together | 128 | `503 Service Unavailable` |
| loopback (127.0.0.0/8 and ::1, one client) | 128, counted apart | `503 Service Unavailable` |

- Both answers carry `Retry-After: 5` and one line of text that says which
  limit it was; nothing of the yard is read for them. An address at its own
  limit is told that (429) even when the total is reached too.
- A stream that ends frees its place at once.
- Loopback is counted apart, so no number of machines on the network
  (`--unsafe`) takes a stream from a page on this machine, and loopback has
  no limit per address: it is one. That is also what every client is
  behind an ssh forward or a proxy on this machine, whose
  `X-Forwarded-For` is not read, since whoever asks writes it.
- An IPv4 address is counted as itself, whatever the port; an IPv6 address
  as its /64, of which one machine has every address.
- Only streams are limited, not pages: a page whose stream is refused is
  served and stands still. htmx's extension asks again by itself, after
  half a second and then twice as long each time up to 64 seconds, and the
  page is live again once a place is free (the next paragraph says what
  then repeats).
- The numbers are constants (`streamMax`, `streamPerAddr` in
  `internal/web/events.go`), not flags. A browser opens six connections to
  a host over all its tabs, so 16 is out of one person's reach, and it
  takes eight addresses to fill 128.

One case repeats entries in the page: htmx's extension opens a new
`EventSource` itself, with the page's first URL and no `Last-Event-ID`,
when the browser gives the old one up, which a browser does when a
reconnect is answered with anything but a stream (another program on the
port, for one). The entries since the page was loaded then come again.
Reload the page.

## Layout

One column, like x.com. Simplicity is the point: **a new view is a new feed
in the centre column, never a new arrangement.**

- **Left**, 200px: the yard's name and a short menu (Home, Yard, Flows,
  Beads, Sessions, Crew members).
- **Centre**, up to 600px: the page.
- **Right**, 300px: what waits for a person, and a summary of the line. It
  is the same on every page.

The page scrolls as a document: no column has a scrollbar of its own, at
any width. The menu stays in view while the page scrolls under it; the
right column scrolls with the page. Wide text that must not wrap (a stage
file, `.text.file`) still scrolls sideways inside its block.

| window | arrangement |
|---|---|
| 1100px and wider | menu, centre, right |
| 700px to 1099px | menu and centre; the right column goes, and a Line entry in the menu leads to `/line` |
| under 700px (a phone) | the centre column alone; the menu is a bar at the bottom, Line included, which stays in view while the page scrolls |

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
| `--faint` | `#b0afb6` | `#625f54` | labels, what is done |
| `--accent` | `#a9ceff` | `#3c5777` | the one accent: the current page, a link in use, focus |
| `--landed` | `#cfa548` | `#8d6830` | the second accent, for what landed and nothing else |
| `--working` | the accent | the accent | status: a session is working |
| `--running` | `#6db58a` | `#2f7d55` | status: idle or running |
| `--held` | `#e0735f` | `#b3402c` | status: blocked or held, and errors |
| `--done` | faint | faint | status: done |
| `--panel-board` | `#202622` | `#f5f1e6` | the panel's track: its board |
| `--panel-ink` | `#e6e1d3` | `#1d2420` | the panel's track: stage and group names |
| `--panel-dim` | `#9aa39b` | `#5d665f` | the panel's track: faint names |
| `--panel-line` | `#d8d2c0` | `#2a2f2b` | the panel's track: the lines |
| `--panel-platform` | `#3a423c` | `#cfc7b2` | the panel's track: platforms and quiet blocks |
| `--panel-signal` | `#e0674e` | `#b5412a` | the panel's track: signals |
| `--panel-mail` | `#e5b74f` | `#c9962b` | the panel's track: the peer wagon |
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
`.rows` (name and number pairs), `.lines` (one line per bead or session, as
the home page's overview shows them), `.depot` (one depot of the overview,
with `.flow-line` under it), `.feed` and `.entry` (the feed and one event
of it, with `.entry-head`, `.entry-what`, `.entry-title`, `.entry-note`),
`.wait` (a bead that waits, with `.wait-title` and `.why`; dimmed inside
`.parked`), `.card` (one bead in a list, the `card`
template, with `.card-head`, `.card-title`, and `.arrived` for one the
stream brought), `.cards` (a list of them), `.column` and `.stage` (a flow
and one stage of it), `.filters`, `.facts` (name and value rows), `.text`
(what someone wrote, lines kept), `.note`, `.outcomes`, `.bead` (a bead's
id), `.state` (a status dot and its word),
`.num`, `.faint`, `.held`, `.landed`, `.dot`, `.diagram` (one drawing of
the yard panel, panning sideways) and `.off-line` (the beads in no stage of
a depot's flows).

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
   page (`head`; `respond` reads one itself only for a page that gives
   none), and its template wraps what changes in an element that reads the
   page again on every event (`templates/sessions.html`):

   ```html
   <div id="live" hx-get="/sessions" hx-trigger="sse:message" hx-select="#live > *" hx-swap="innerHTML ignoreTitle:true">
   ```
4. **The route**, in `routes` (`server.go`), with its method:
   `s.mux.HandleFunc("GET /beads", s.handleBeads)`. For a page the menu
   already links to, take its name out of `unbuilt`.
5. **Tests** in `web_test.go`, with `httptest`: the JSON and the HTML of the
   same request. `TestNoOtherHost` walks every link from Home, so a new
   page is checked for other hosts and inline styles once the menu or a
   page links to it; add the page to those the walk must reach.
6. **This file**: the row in Pages, and the JSON's shape.

A template that is a part of a page and no page (`shell`, `aside`, `entry`,
`card`) is named in `partial` (`server.go`), and every page is parsed with
it.

A page that must follow the yard has two ways, both htmx's and neither a
script of its own, and both on the shell's one stream: a page never opens
a second. One that can simply be read again repeats its own request at
each event, as `/beads` does (`hx-trigger="sse:message"`, with `hx-select`
for the part to replace); `stream: "seq"` on its `Page` keeps the stream
to the seqs. One where a single thing moves has the stream carry that
thing, as the feed and `/flows` (`stream: "card"`) do. Either way the
handler reads the head before the data and gives it to the `Page`, so
nothing between the page and the stream is lost.

A page adds no script and no stylesheet. If it needs a new class, it goes
in `yard.css` and uses the tokens.
