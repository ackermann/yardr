# The web view

`yardr web serve` shows the yard in a browser. It is a client of the yardr
server, as `yardr bdp serve` is. Without `--as`, `YARDR_WEB_AS` or the
`web_as` setting it reads only: no page takes a write, and every route
refuses any method but GET and HEAD. With one of them it acts as that
user too: the talk page sends its mail as them, and a bead's page
answers its asks and sends the bead on
([Acting as a user](#acting-as-a-user)), and nothing else takes a
write.

```sh
yardr web serve                          # http://127.0.0.1:8791/
yardr web serve --addr 127.0.0.1:9000
yardr web serve --as mckean              # acts as mckean: talk, asks, moves
curl -s localhost:8791/                                  # the page
curl -s -H 'Accept: application/json' localhost:8791/    # the same data
```

## The address

The view listens on `--addr` when it is given, else on `YARDR_WEB_ADDR` when
set in the command's environment, else on the yard's `web_addr` setting,
else on `127.0.0.1:8791`. `yardr web serve`, `yardr web start` and
`yardr web install` ([Keeping it running](#keeping-it-running)) all go by
that order, so a yard that sets the address needs no flag at any of them:

```sh
yardr settings set web_addr 127.0.0.1:8792   # a second yard on this machine
yardr web start                              # http://127.0.0.1:8792/
YARDR_WEB_ADDR=127.0.0.1:8793 yardr web start # the environment overrides the setting
yardr web start --addr 127.0.0.1:9000        # --addr still wins
```

The setting takes what `--addr` takes on loopback (next) and refuses the
rest with the same sentence. It moves nothing that runs: a view keeps its
address until it is stopped and started again, and a login service keeps
the one in its file until `yardr web install` is run again. `yardr settings
set web_addr` says so, and `yardr web status` prints the address of each
view that runs. `yardr bdp serve` has `bdp_addr` the same way
([configuration.md](configuration.md#settings)).

The user the view acts as resolves the same way: `--as` when it is
given, else `YARDR_WEB_AS` when set in the command's environment, else
the yard's `web_as` setting, else read-only
([Acting as a user](#acting-as-a-user)). `yardr web serve`,
`yardr web start` and `yardr web install` all go by that order, so a
yard that sets the user needs no flag at any of them:

```sh
yardr settings set web_as mckean   # the view acts as mckean
yardr web start                    # acts as mckean: talk, asks, moves
YARDR_WEB_AS=jo yardr web start    # the environment overrides the setting
yardr web start --as jo            # --as still wins
```

The sandbox origin, where a note's pages run ([One bead](#one-bead)),
listens on `--pages-addr` when it is given, else on
`YARDR_WEB_PAGES_ADDR` when set in the command's environment, else on
the yard's `web_pages_addr` setting, else on `127.0.0.1:8789`, on
loopback like the view and under the same Host rules (`--unsafe` opens
it on every interface too, on its port). A second yard on this machine
sets both addresses, as the view's address alone no longer moves it
off the first yard's ports:

```sh
yardr settings set web_addr 127.0.0.1:8792
yardr settings set web_pages_addr 127.0.0.1:8788   # its sandbox origin
```

## Loopback only

By default the view listens on the loopback interface and nowhere else;
an address on another interface is refused unless `--unsafe` is given
(below):

- `--addr` takes `127.0.0.1`, `::1` or `localhost` with a port. Any other
  address, and one with no host (`:8791` listens on every interface), is
  refused before the yard is touched, with a message that says why.
- A request whose `Host` header is not a loopback name (`localhost`,
  `127.0.0.1`, `[::1]`) or a name given with `--host`
  ([Behind a proxy](#behind-a-proxy)) is answered 421. A page on another
  site can otherwise point a name of its own at 127.0.0.1 and read the
  yard through the visitor's browser (DNS rebinding). The 421 says which
  names are answered and, for a name that could be a host's, how to add
  it:
  `... ask for it as localhost or 127.0.0.1, not yard.example.net:8791. If that name is this machine's, start the view with --host yard.example.net`.

To look at a yard on another machine, forward both ports over ssh
(`ssh -L 8791:127.0.0.1:8791 -L 8789:127.0.0.1:8789 host`; the second
forward carries `run` pages), or put a proxy of your own in front of the
view (next).

The sandbox origin beside the view is fenced the same way: loopback
only, the same Host rules and 421s, and it answers to its public name
(`--pages-host`) besides, where one is given.

### Behind a proxy

`yardr web serve --host <name>` without `--unsafe` leaves the view on
loopback, exactly as above, and answers to that name as well as the
loopback names. It is for a view with a proxy of yours in front of it on
the same machine: the proxy takes the request from elsewhere under its own
name and hands it to the view on loopback, and the view answers because you
said that name is this machine's.

```sh
yardr web serve --host yard.example.net   # still 127.0.0.1:8791, answers to yard.example.net too
```

yardr knows nothing of the proxy: it sets none up, looks for none, does no
TLS and, outside JWT mode, reads no header a proxy adds
(`X-Forwarded-Host`, `X-Forwarded-For`, a user's name). In JWT mode it
reads the one configured header (`web_jwt_header`) and verifies the signed
token it carries ([Behind an auth gateway](#behind-an-auth-gateway)). What
it asks of one, whichever it is:

- **Forward the `Host` header unchanged.** The view compares the header it
  gets with the names it was given, in any case and with any port. In nginx
  that is `proxy_set_header Host $host`. A proxy that rewrites `Host` to a
  loopback name (`localhost`, `127.0.0.1`) needs no `--host` at all.
- **TLS and access are the proxy's.** The view speaks plain HTTP to
  loopback and has no login: whoever the proxy lets through reads the whole
  yard. Who that is, the proxy says (a private network's membership, a
  login of its own).
- **Name only names whose resolution you control**: the machine's name on a
  private network of yours, a name in DNS you run, a name you assigned at
  the proxy. Under a name someone else controls, their pages could point it
  at 127.0.0.1 and read the yard through a visitor's browser, which is what
  the 421 is there to stop.

What the view answers to is then the loopback names and the names given,
and nothing is added to them:

- A name not given is 421, and the text names the flag.
- This machine's own host name, with or without `.local`, is 421 unless you
  give it with `--host`: only `--unsafe` answers to it unasked.
- An IP address off loopback in the `Host` header is 421, and `--host` with
  one is refused: that is `--unsafe`'s.
- A wildcard (`*`, `*.ts.net`) and an empty name are refused, as under
  `--unsafe`: a view that answers to every name has no guard left.

The flag takes one name or a comma list and may be repeated
(`--host yard.example.net --host yard.lan`). The view says what it answers
to when it starts:
`serving the yard url=http://127.0.0.1:8791/ pages=http://127.0.0.1:8789/ answers_to="a loopback address, localhost, yard.example.net"`.
`yardr status` and `yardr web status` still call such a view
`this machine only`: loopback is all it listens on. Every client that comes
through the proxy is the one loopback client to the stream limits
([The stream](#the-stream), **How many**). `yardr web start` and
`yardr web install` take the same `--host`, for a view no terminal runs
([Keeping it running](#keeping-it-running)).

One example, with Tailscale's `tailscale serve`, which shares a port on
loopback with the devices of your tailnet over HTTPS. Any other proxy that
keeps to the three points above does as well.

```sh
yardr web serve --host adventuretime.tail5cf2e8.ts.net   # the machine's name on the tailnet
tailscale serve --bg 8791                                # https://adventuretime.tail5cf2e8.ts.net/ -> 127.0.0.1:8791
```

The `tailscale serve` line is as the help of Tailscale 1.102.4 gives it
(read 12 Oct 2026) and has not been run against the view yet, and that
help does not say which `Host` the view then sees. Start the view without
`--host` first: if the page opens under the tailnet name, `tailscale serve`
rewrote `Host` and no `--host` is needed; if it is 421, the text names the
`--host` to start it with.

A proxy in front of the view forwards the sandbox origin's port too:
run links name the view's host on the origin's port
(`//yard.example.net:8789/blobs/<hash>`), so the proxy passes that port
to where the origin listens, as it passes the view's, with the Host
header unchanged. Where the origin should have a name of its own
instead, route a second name to it and give that name with
`--pages-host` (`YARDR_WEB_PAGES_HOST`, or the yard's `web_pages_host`
setting, as `--pages-addr` goes by its own three):

```sh
yardr web serve --host yard.example.net --pages-host pages.yard.example.net
```

Run links then use the second name on the port the view was reached by
(`//pages.yard.example.net/blobs/<hash>`), and the origin answers to it
besides the view's names. Name only a name whose resolution you control,
as with `--host`; the loopback rules above hold for the origin's own
port either way.

### Behind an auth gateway

With an auth gateway in front of the view — one that logs the visitor in
and forwards each request with a signed JWT proving who they are — the view
verifies that token on every request but `/health`, and maps its email to
a user of the yard. Setting `web_jwt_issuer` turns this JWT mode on:

```sh
yardr settings set web_jwt_issuer https://team.cloudflareaccess.com
yardr settings set web_jwt_audience <the application's AUD tag>
yardr settings set web_jwt_jwks https://team.cloudflareaccess.com/cdn-cgi/access/certs
yardr settings set web_jwt_users mckean@example.net=mckean
yardr web install --host yard.example.net
```

Each gateway is one row, not one code path: the header carrying the token,
the issuer (`iss`) and audience (`aud`) it must carry, and the URL its keys
are fetched from.

| Gateway | Header (`web_jwt_header`) | Issuer (`web_jwt_issuer`) | Keys (`web_jwt_jwks`) | Audience (`web_jwt_audience`) |
|---|---|---|---|---|
| Cloudflare Access | `Cf-Access-Jwt-Assertion` | `https://<team>.cloudflareaccess.com` | `https://<team>.cloudflareaccess.com/cdn-cgi/access/certs` | the application's AUD tag |
| oauth2-proxy | `Authorization` (the ID token as `Bearer`, with `--pass-authorization-header`) | the provider's issuer | the provider's `jwks_uri` | the OAuth client id |
| Pomerium | `X-Pomerium-Jwt-Assertion` | `https://authenticate.<yours>` | `https://authenticate.<yours>/.well-known/pomerium/jwks.json` | the route's domain |

Cloudflare mirrors the token as a `CF_Authorization` cookie too, but the
header is authoritative and the only carrier the view reads. oauth2-proxy
also passes an access token as `X-Forwarded-Access-Token`
(`--pass-access-token`); that is a JWT only with some providers, so prefer
the ID token. Its `X-Forwarded-User` and `X-Forwarded-Email` stay untrusted,
as every bare header does: anything on loopback can set one. Pomerium sends
its assertion only with identity headers enabled, globally or for the route
(`pass_identity_headers`, false by default).

Whose email the view trusts is the operator's call: a signature proves the
gateway asserted the address, not that its identity provider checked the
visitor owns it, so pick a gateway whose provider verifies emails. A token
whose `email_verified` claim is present and false is refused.

In JWT mode `--as`, `YARDR_WEB_AS` and `web_as` are refused at start — no
request is ever served as the default user — and so is a start without
`web_jwt_audience` or `web_jwt_jwks`, or while the keys cannot be fetched.
A request without the header, or with a token that is malformed, badly
signed, expired, of another issuer or for another audience, is 401; a valid
token whose email maps to no user is 403.

Each request then acts as its own user: the talk page holds the visitor's
conversation and sends mail as them, an ask answers where it is addressed
to them, a bead sends on where it waits for them, and the home and line
needs queue shows each visitor only the asks addressed to them — two
visitors act as themselves on one view. The POST routes exist in JWT mode
as with `--as`, and a form's POST is still taken only from the view's own
pages ([Acting as a user](#acting-as-a-user)). The event stream closes at
the token's expiry, and the client reconnects with a fresh token. `yardr
status` and the start line say whose tokens the view verifies (`jwt from
<issuer>`).

### `--unsafe`: every interface

`yardr web serve --unsafe` listens on `0.0.0.0:8791` (on the selected
address's port), every interface, so
that a phone on the same LAN can open `http://<this machine's IP>:8791/`.
Outside JWT mode there is still no authentication: anyone who can reach this
machine on that port can read the whole yard, and act as the user of a view
that acts. In JWT mode every request but `/health` needs a token the gateway
signed.
With none of `--as`, `YARDR_WEB_AS` and `web_as` the view stays
read-only.

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
  (`--host yard.example.net --host yard.lan`). A wildcard (`*`,
  `*.ts.net`) and an empty name are refused: a view that answers to every
  name has no guard left. Without `--unsafe` the flag names hosts for a
  view on loopback ([Behind a proxy](#behind-a-proxy)).
- The 421 says which names are answered and, for a name that could be a
  host's, how to add it:
  `... not yard.example.net:8791. If that name is this machine's, start the view with --host yard.example.net`.
- The event streams one address may hold open, and all addresses
  together, are limited ([The stream](#the-stream), **How many**), so a
  device on the network cannot hold streams open without end. Loopback's
  are counted apart.
- Outside JWT mode it logs one warning when it starts:
  `no authentication: anyone who can reach this machine on port 8791 can read the yard`.
  On a view that acts it says `can read the yard and act as <user>` instead.
- In JWT mode the warning says requests other than `/health` need a token
  signed by the gateway.
  The line after it says what the view answers to:
  `serving the yard url=http://[::]:8791/ pages=http://[::]:8789/ answers_to="an IP address, localhost, adventuretime, adventuretime.local, yard.example.net"` when started with `--unsafe --host yard.example.net`.

## Acting as a user

The view acts as `--as` when it is given, else as `YARDR_WEB_AS` when
set in the command's environment, else as the yard's `web_as` setting
(`yardr settings set web_as <user>`), and reads only with none of them.
`yardr web serve`, `yardr web start` and `yardr web install` all go by
that order: a start and an install hand on `--as` only when it is
given, so a view of theirs without it reads the setting itself, at
every start. Whoever the view acts as must be a user of the yard
(`yardr user list`): with any other name it refuses to start, naming
the flag, the variable or the setting the name came from. The talk
page ([Talk](#talk)) then holds the user's conversation with a crew member,
live, with the form that sends it mail as them through the socket's `Mail`
([api.md](api.md#mail)), as that user. An ask to the user answers with
buttons, on the needs rows and on its bead's page, through the socket's
`AnswerAsk`, `DeferAsk` and `DeclineAsk`; a bead in a stage that waits for
the user sends on as `yardr review` offers it, with a note
([One bead](#one-bead)). Every other route stays GET, and with none of
them every route is GET as before: the talk page then says the view does
not act, and a test holds the view to it.

```sh
yardr settings set web_as mckean   # the view acts as mckean, with no flag
yardr web serve --as mckean
yardr web install --host adventuretime.tail5cf2e8.ts.net --as mckean   # behind tailscale serve, for a phone
```

Each write carries the revision the page was drawn from: one from a page
the bead has since left behind is refused, and the page comes back redrawn
with why, so two tabs answering the same ask leave one answer and one
refusal.

A form's POST is taken only from the view's own pages: a page on another
site cannot make a visitor's browser act as the view's user. What is
compared is the request's `Origin` header, and this is how it is matched:

- The scheme is `http` or `https`: `https` where an edge stands in front
  and ends TLS there, `http` to the view itself. Anything else is refused,
  and so is a missing `Origin`.
- The host must match the request's `Host` (case-insensitively). That host
  is already checked against the names the view answers to: a loopback name
  or a name given with `--host` ([Behind a proxy](#behind-a-proxy)); with
  `--unsafe`, it may also be an IP address. Other names the view answers to
  are still different origins, so a page at `localhost` cannot post to a
  request addressed as `yard.example.net`.
- The explicit port must match the request's `Host`. A proxy in front
  forwards that host unchanged, so its external port is checked here; it
  need not be the view's own listening port. The scheme is not compared:
  the edge may end TLS and forward the request to the view over HTTP.

Anything else is answered 403 and sends nothing. `Origin` is the browser's
own header: a client that is no browser sends none, so `curl` posts with
one of its own (`-H 'Origin: http://127.0.0.1:8791'`).

The view still has no login: whoever reaches it acts as its user, and who
reaches it is the edge's to say — the private network's membership, a login
of its own — as who reads the yard is ([Behind a proxy](#behind-a-proxy)).
The edge is what stands in front. `yardr status` says whom a view acts as
(`this machine only, acts as mckean`), and the start line carries it too
(`serving the yard url=http://127.0.0.1:8791/ pages=http://127.0.0.1:8789/ acts_as=mckean`), whether
the flag, the variable or the setting named the user. Behind an auth
gateway the view acts per request instead of as one user: each visitor's
token maps to their own user ([Behind an auth
gateway](#behind-an-auth-gateway)).

## Keeping it running

`yardr web serve` runs while its terminal does. Two commands run it without
one, both on loopback only (`--addr` takes a loopback address with a port,
default the yard's `web_addr`, else `127.0.0.1:8791`
([The address](#the-address)); neither has `--unsafe`):

```sh
yardr web start                  # in the background, until it is stopped or the machine restarts
yardr web stop                   # stop every view that runs on the yard
yardr web status                 # the views that run, and whether a service starts at login
yardr web install                # starts now, and at every login
yardr web install --addr 127.0.0.1:9000
yardr web install --host yard.example.net   # answers to that name too, for a proxy of yours
yardr web install --as mckean               # acts as mckean: talk, asks, moves
yardr web uninstall
```

Both take `--host` as `yardr web serve` does on loopback
([Behind a proxy](#behind-a-proxy)): one name, a comma list, or the flag
again, each handed on to the view, which answers to those names besides the
loopback names. It gives the view names, not interfaces: the view still
listens on loopback only, and `yardr status` still says "this machine only"
of it. What `yardr web serve` refuses on loopback is refused here before
anything is started or written: a wildcard, an empty name, a name with a
scheme or a port, an IP address off loopback. `yardr web install` writes
each name into the service's file as a `--host` of its own, so the view the
service starts at every login answers to them; to change the names, run
`yardr web install` again with the ones you want.

Both take `--as` as `yardr web serve` does
([Acting as a user](#acting-as-a-user)): the view acts as that user.
`yardr web install` writes it into the service's file beside the names.
Without `--as` the view acts as `YARDR_WEB_AS`, else the yard's `web_as`
setting. A login service keeps the `YARDR_WEB_AS` value from when it was
installed; when that variable is unset in the service, its view reads
`web_as` at every start, so changing the setting needs no reinstall. The
view refuses to start when no user of the yard has the name, whatever named it; `yardr web
start` then ends at once and names the log the reason is in, and the
service's view logs it at every login.

Both take `--pages-addr` and `--pages-host` as `yardr web serve` does
([The address](#the-address)): the sandbox origin beside the view
listens on one and is named by the other. `yardr web install` writes
each into the service's file when it is given; without them the view
the service starts goes by `YARDR_WEB_PAGES_ADDR` and
`YARDR_WEB_PAGES_HOST` from its install environment, else the yard's
`web_pages_addr` and `web_pages_host` settings, read at every start.

`yardr web start` starts `yardr web serve` detached, logging to
`<home>/web.log`, waits until it listens and prints its address, its pid and
how to stop it. It refuses an address something listens on already.

`yardr web stop` stops the views that the records under `<home>/webviews/`
name ([below](#where-it-runs)), however each was started: in a terminal, by
`yardr web start`, or by the service that starts when you log in. It signals each and waits
(`--wait`, default 30s) until the view has let go of its record, then names
each with its pid and address; with none running it says so. A `yardr web
start` after it answers at the same address. A view that service ran
stays stopped until the next login, since it ended without a failure;
`yardr web uninstall` is what removes the service. The verbs are the same
for every long-running command
([configuration.md](configuration.md#long-running-commands)).

`yardr web install` writes the view as a service that starts when you log in,
launchd agent on macOS and a systemd user unit on Linux, and loads it: the
view is started now and at every login, and started again if it ends with a
failure. It prints the address. The file, its name, `--file-only` and
`yardr web uninstall` are as for the daemon
([configuration.md](configuration.md#login-services)). Stop a view started
with `yardr web start` before installing one on the same address: the
service's cannot listen while the other does, and is tried again every 10
seconds. `yardr upgrade` restarts the service on the build it installs, so
the view serves the new pages without a reinstall; a `go install` by hand
leaves the running view on its build until the service is restarted.

The onboarding offers both once the yard runs: "Start the web view now?"
and, on a yes, "And at login?", each starting at no.
[`yardr doctor`](doctor.md#optional) reports the view as configured once the
service is installed, with its address.

## Where it runs

`yardr status` lists every web view that runs on the yard, however it was
started: the URL to open, its pid, the build it runs, who can open it, and
whom it acts as.

```
web view   http://127.0.0.1:8791/     pid 4801  build abcdef123456  this machine only, acts as mckean
           http://192.168.1.20:9000/  pid 4950  build abcdef123456  open to the network (--unsafe), no login
           service that starts at login: not installed (yardr web install)
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
  started again. `yardr upgrade` restarts the service that starts when you
  log in on the new build; a view started by hand stays on its build until
  it is stopped and started again. When that is another build than the
  yardr that was asked, a line under the views says so (`this yardr is
  build <build>: ...`).
- The views are read from the home, not from the server, so status lists
  them while the server is stopped too, and starts none.
- While it serves, a view keeps a record in `<home>/webviews/` (its
  address, its pid, `--unsafe` or not, the user it acts as, when it started,
  the build it runs)
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
none: scripts and styles live in the asset files. It refuses every form
target (`form-action 'none'`) but on a view that acts, whose talk and bead
pages post to the view itself (`form-action 'self'`). The one exception
to the test is a bead page's run links, which name the yard's own sandbox
origin; the test allows those and only those ([One bead](#one-bead)).

## Pages

| path | page | JSON |
|---|---|---|
| `/` | Home: the overview — the answer line with one row per wait, then the Running, Yard and Closed rows — above the feed of what happened, newest first and live | `Home` |
| `/?before=<seq>` | an older page of the feed: the 50 entries under that seq, without the overview | `Home` |
| `/talk` | on a view that acts: the user's conversation with a crew member, live, and the form that sends it mail as them; without a user, that the view does not act ([Talk](#talk)) | `Talk` |
| `/talk?with=<member>` | the conversation with that member; without `?with=` the yardmaster's, else the first member's | `Talk` |
| `/talk?with=<member>&before=<seq>` | an older page of the conversation: the 50 letters under that seq, a snapshot | `Talk` |
| `/events` | the yard's events as they happen, as server-sent events ([The stream](#the-stream)) | `?format=json` |
| `/line` | the right column as a page, for a window too narrow for the column: what waits for a person, and the line in numbers ([The right column](#the-right-column)) | `Line` |
| `/line?home=1` | the home mode: the line panel alone, what home's aside shows and re-reads ([The right column](#the-right-column)) | `Line` |
| `/flows` | per depot, the flows that govern it: each stage a section with its group and the open beads in it as cards, which move as the beads do ([Flows](#flows)) | `Flows` |
| `/flows?panel=1` | the yard panel: every depot's flows drawn as track, with the open beads standing on them as blocks ([Flows](#flows)) | `Flows` |
| `/beads` | the open beads, the newest change first ([Beads](#beads)) | `Beads` |
| `/beads?depot=<depot>&stage=<stage>` | the same, of one depot, in one stage, or both | `Beads` |
| `/beads?all=1` | the closed beads too, every one (`yardr bead list --all`) | `Beads` |
| `/beads/<id>` | one bead: what `yardr prime --bead <id>` knows, for a person ([One bead](#one-bead)) | `BeadPage` |
| `/blobs/<hash>` | a note's image or page: the bytes of a blob a note on some bead names, with their type; anything else is a 404 ([One bead](#one-bead)). On the sandbox origin the same bytes run under the scripted policy | |
| `/sessions` | the running sessions, the oldest first, with bead, stage, group, originator, runner, state and age (`yardr session list`), and under them the claims of manual groups, live | `Sessions` |
| `/sessions?all=1` | the same page; its JSON lists the ended sessions too, every one (`yardr session list -a --all`) | `Sessions` |
| `/crew` | each crew member: whether it runs, since when, and its unread inbox count when there is one, live. Journals and handoffs are not shown | `Crew` |
| `/config` | the hub of the configuration pages: links to the six lists | `Config` |
| `/depots` | the yard's depots: each with its kind, path, base and test command (`yardr depot list`), live | `Depots` |
| `/groups` | the yard's groups: each with its runner, limit, members and config (`yardr group list`), live | `Groups` |
| `/routes` | the yard's routes in match order: each with its stage, filters, priority and group, or its groups in order with their quota conditions (`yardr route list`), live | `Routes` |
| `/peers` | the yards this one is linked with: each with its URL, what the link carries and what waits for it (`yardr peer list`), live | `Peers` |
| `/deps` | what waits on what: the edges with an open end, grouped by the blocked bead, each end with its title and stage, live | `Deps` |
| `/deps?all=1` | the same page; its edges are every edge of the yard, the ones between closed beads too | `Deps` |
| `/limits` | what the subscriptions the yard burns still carry: each provider in use with its windows, how far each is used and when it resets, and who burns it (`yardr limits`), live | `Limits` |
| `/health` | the yard's liveness facts for a monitor, a proxy or footplate ([Health](#health)) | `Health` |
| `/assets/<file>` | the stylesheet and the two scripts | |

The feed, the right column, `/flows`, `/beads`, a bead's page, the sessions,
the crew, the talk page's newest letters and the depot, group, route, peer,
edge and quota lists are live
([Live pages](#live-pages)). The overview above the feed is a snapshot:
reload the page to see it change.

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
- On the page the depots are one row to the panel: `Yard N · working M ·
  held H` to `/flows?panel=1`, shown only when N is not zero, with
  `working` and `held` shown only when they are not zero. N, M and H sum
  the depots' `open`, `working` and `held` above. The page prints no zero.

- `attention` are the open beads that wait for a person: everything
  something stops (held, blocked on a prompt, stranded), the beads with
  an open ask to the operator — to the view's user, on a view that
  acts — and the parked beads past their flow's first, in the
  line's order, faults first. A bead parked where it starts (a backlog)
  is on `/line` only, unless an ask names the reader in particular.
- On the page they stand under the answer line: `Nothing needs you.`
  with `All clear.` under it when none waits, else `N need you.` with
  the faults counted apart (`2 need you. 1 fault.`). Each wait is one
  row, a link to its bead: plain words first (`held`, `gave up`,
  `blocked`, `stranded`, or the stage for a parked bead), the title,
  the bead's short id small at the row's end, and how long it has
  waited; a fault carries a red dot. An asked bead stands once: its
  fault first when it has one, then the question where the title stands,
  the full bead id and age, and optionally the bead's first `topic:` label
  small. A recommendation appears once in the labelled
  detail below the row. On a view that acts the oldest ask answers
  under the row, with its options as buttons ([Acting as a user](#acting-as-a-user));
  otherwise the row only links to the bead. On a phone an asked row gives the
  question the row's first line whole, ending in an ellipsis, and the
  bead id, optional topic and age stand under it, so the question
  never gets less room than the metadata. The reasons, the titles and
  the questions render in the proportional face; ids and ages stay
  monospace. Past five the rest stand behind `All waiting N`, to
  `/line`. The JSON names every wait either way.
- `running` are the sessions running now, the oldest first: the rows of
  `/sessions` without the claims of manual groups, which run nothing.
- On the page they are one row: `Running N · oldest 3h` to `/sessions`,
  shown only when N is not zero; the age is how long the oldest has run.
- `closed` are the beads that closed today, the newest first, at most ten.
- On the page they are one row: `Closed today N` to `/beads?all=1`, shown
  only when N is not zero. The titles stay in the JSON.

- `events` is the feed, newest first: at most 50 entries, one per event
  ([The feed](#the-feed) says what each field holds).
- On the page they are ten one-line rows at most, the kept events, each
  title in the proportional face, with `All activity` to `/?all=1`
  behind them. The JSON names every entry either way.
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
     "train": "yardr-e3rg", "stage": "new", "group": "yardr-builders", "originator": "hal",
     "runner": "herdr yardr-68k3-new",
     "state": "running", "blocked": true, "started_at": "2026-10-04T00:02:11Z", "age": "3h 4m"}
  ],
  "manual": [
    {"id": "t0m3xq8c4k2v9d5h1w7a", "bead": "yardr-41ma", "title": "name the command",
     "stage": "decide", "group": "decisions", "originator": "jo", "runner": "manual",
     "state": "claimed", "started_at": "2026-10-03T23:58:07Z", "age": "3h 8m"}
  ],
  "list": [
    {"id": "vkqehjw2rt151h7s0xy7", "bead": "yardr-68k3", "group": "yardr-builders", "stage": "new",
     "runner": "herdr", "kind": "claude", "model": "opus", "state": "running", "handle": {"agent": "yardr-68k3-new"}, "stopped": false,
     "started_at": "2026-10-04T00:02:11Z", "train": "yardr-e3rg", "originator": "hal"},
    {"id": "t0m3xq8c4k2v9d5h1w7a", "bead": "yardr-41ma", "group": "decisions", "stage": "decide",
     "runner": "manual", "state": "running", "handle": {"manual": true}, "stopped": false,
     "started_at": "2026-10-03T23:58:07Z", "originator": "jo"}
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
- `originator` is the user the bead is for. A subsidy route may use the
  operator's login for that user's work.
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
     "agent": "herdr · claude · opus", "kind": "claude", "provider": "claude", "unread": 3},
    {"name": "night", "state": "stopped", "running": false, "enabled": false,
     "agent": "herdr · claude", "kind": "claude", "provider": "claude", "unread": 0}
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
- `provider` is the subscription it burns: its `provider` setting where
  set, even where no source answers for it, else its kind's mapping
  through the first source by name that lists it; absent where neither
  answers and for a crew member of another runner. Resolving runs no
  command.
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
| `yardr group list` | `/groups` | `groups`, each row with its resolved `provider` |
| `yardr route list` | `/routes` | `routes`, in match order |
| `yardr peer list` | `/peers` | `peers` |
| `yardr dep list <bead>`, for every bead | `/deps` | `deps`: the edges with an open end, the oldest first, each end with its title, stage and status; `groups`: the same grouped by the blocked bead |
| `yardr dep list <bead>`, for every bead | `/deps?all=1` | `deps`: every edge of the yard once, the oldest first, the ones between closed beads too |
| `yardr bead list` | `/beads` | `beads` |
| `yardr bead list --all` | `/beads?all=1` | `beads` |
| `yardr session list` | `/sessions` | `list` |
| `yardr session list -a --all` | `/sessions?all=1` | `list` |
| `yardr crew list` | `/crew` | `crew`, in part |
| `yardr bead history <bead>` | `/beads/<bead>` | `history` |
| `yardr flow show <depot>` | `/flows` | `depots[].flows`, as the page has them ([Flows](#flows)) |
| `yardr limits` | `/limits` | `limits` with `unmapped`: what the command answers with no kind named, one entry per provider in use, with the burning kinds and users beside the report |

```json
{
  "groups": [
    {"name": "yardr-builders", "runner": "herdr", "config": {"kind": "claude", "model": "opus"}, "limit": 3, "provider": "claude"},
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
  `since`, `up`, `agent`, `kind`, `provider`, `unread` and `restart`
  beside them. The page's fields keep their names and meanings.
- A group of `/groups` has every field of `group list --json` and the
  resolved `provider` beside them: the group's `provider` setting where
  set, even where no source answers for it, else its kind's mapping
  through the first source by name that lists it; absent where neither
  answers and for a group of another runner. The one exception to the
  same-fields rule, following `/crew`'s page fields; resolving runs no
  command. `group list --json` itself is unchanged: the setting shows
  inside `config`.
- An edge of `/deps` has every field of `dep list --json` and each end's
  title, stage and status beside them, under `from_bead` and `to_bead`.
  `groups` carries the page's grouping: one blocked bead's card with the
  edges that hold it, the longest waiting first. `total` is the yard's
  edges in all, what `?all=1` lists.

`/depots`, `/groups`, `/routes`, `/peers`, `/deps` and `/limits` have one
page each, under the menu's Config entry: the lists as their commands
print them, each marking that entry current and linking back to the hub
(`/config`), which links to all six. `?all=1` is a list
command's `--all`: every closed bead, every ended session the yard keeps,
every edge between closed beads, with no limit. Any value but none, `0`
or `false` asks for it.

`/limits` is the one route a GET may run the operator's command for. Each
entry is the command's: the provider, the burning kinds and users under
`kinds` and `burned_by`, the provider's report under `limits`, the
used-up window under `window` and `resets_at`, or the reason under
`limits_unknown`, with `unmapped` naming the kinds in use that resolve to
nothing. A read asks the source at most once a minute per provider, however
often the route is read: reads in between share the last answer, and
concurrent reads the one run on its way. The page reads through the same
handler, so its live re-read asks nothing extra. Each run is bounded by the 30-second Timeout,
as the command's is, and a provider that fails nulls its entry without
failing the route; a provider no source answers for is not asked, and is
not known with why. The answer is the same on loopback and with `--unsafe`.

The table is where the rule holds today. The other list commands (`timer`,
`hook`, `source`, `user`, `link`, `pack`, `thread`, `review` and `flow
list`) have no route yet, and neither has `yardr events -n <N>`: the
log is [the stream](#the-stream), which replays what is retained from
`?after=<seq>`. A new list command gets its route with it, and an old one
when a client needs it: a handler in `lists.go` that ends in `listedPage`, the
route in `routes`, a row here.

### JSON or HTML

Every page answers both from one value. A request gets JSON when its
`Accept` header names `application/json` (or `application/*`) with a higher
weight than `text/html`; everything else gets the page, so a browser, plain
`curl` and `*/*` all get HTML. Responses carry `Vary: Accept`.

Errors keep the form: a 404, a 501 or a 500 is the error page inside the
shell for a browser and `{"error": "..."}` for a JSON client (an unbuilt
page: `{"page": ..., "built": false}`).

## The right column

The same on every page but home, and `/line` as a page of its own where
the window has no room for it. Home's aside is the line panel alone:
home lists the waits at the top of its centre already, so the aside
shows only the numbers. `/line?home=1` is that home mode as a page,
what home's aside shows and re-reads, while `/line` itself still
renders the waits and the line whole. The parameter shapes the HTML
alone: as JSON the column answers whole either way. As JSON (`/line`):

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
  "late": [
    {"bead": "yardr-68k3", "title": "web: what waits for a person", "depot": "yardr", "stage": "new",
     "since": "2026-10-04T00:02:11Z",
     "late": {"stage": "new", "clock": "running", "in_stage": "6h", "p90": "2h",
              "line": "late: running in new 6h, past the usual 2h (p90 claim-to-exit for difficulty:2 in yardr-builders)"}}
  ],
  "open": 6, "working": 2, "held": 1, "sessions": 2,
  "stages": [{"stage": "backlog", "open": 3}, {"stage": "new", "open": 2}, {"stage": "decide", "open": 1}],
  "depots": [{"name": "yardr", "open": 6}]
}
```

`waits` and `parked` are the open beads that wait for a person, each with
every reason (`why`) that holds for it:

| `kind` | the bead | `text` |
|---|---|---|
| `held` | is on hold | `held by <actor>`, and as `note` the reason: the note that actor left right before, as in [the feed](#the-feed). When the bead's events hold a `gave_up` for the hold, the text names it instead: `gave up after 3 failures in new` |
| `blocked` | has a running session that waits on a permission prompt or a question | `its session waits on a prompt: <agent>`, the agent's name in its runner, else the session's id |
| `stranded` | sits in a stage its flow lacks (`yardr flow show <depot>`) | `stranded in <stage>, a stage flow <flow> lacks` |
| `manual` | is in a stage of a manual group: the group that claimed it, else the one its stage is routed to | `waits for <group> (<members>) in <stage>` |
| `ask` | carries an open ask to the operator | the question, and as `note` its context lines |

- A bead whose only reason is `manual` is in `parked`, every other one in
  `waits`: nothing stops a parked bead, it sits in a stage that is a
  person's by design (a backlog, a decision). The column lists the parked
  under what waits, in a part headed "Parked" with their count; "Nothing
  waits for you." shows when both are empty. A parked bead that is also
  held or stranded is in `waits`, with its `manual` reason too, as is an
  asked bead, with its question after its fault.
- A hold after repeated failures still waits once: the bead stands in
  `waits` with the give-up in its reason, not beside its hold.
- An ask waits once per bead whatever else waits with it. Only an open
  ask to the operator stands: one answered, declined, put aside or taken
  back, one to another person, and one on a closed bead or from another yard
  stands in no row. A yard that names no operator waits without asks.
- In both, the bead that changed last is first; `since` is that change,
  about when it began to wait.
- Whether a session is blocked is kept nowhere but in the events: the
  latest `session_blocked` or `session_unblocked` among the bead's last
  200 that names the session decides. A session that left its prompt while
  no daemon ran gets its `session_unblocked` on the next daemon's first
  pass, and shows blocked until then.
- When the yard cannot say which beads are stranded, the error is logged
  and the column shows the rest.

`late` are the open beads running past their usual time, the newest
change first: `/line`'s own rows under the waits, in a part headed
"Late" with their count. A late bead needs nothing yet, so they stand
apart from the waits and the needs count. Only the `/line` page reads
them; the shell's right column on every other page leaves them out. Each
row carries the bead's estimate: `late` is always set, `eta` beside it
when there is anything to say beyond the mark
([Estimates](#estimates-the-eta-and-the-late-mark)).

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
(`Page.stream`) and the shell adds `&format=`: `home` on `/`, `card` on
`/flows`, `seq` on `/beads`, a bead's page and the depot, group, route,
peer, edge and quota lists ([The stream](#the-stream)).
Whatever the format, an event is one unnamed message, which is all the
right column listens for. These hang on the stream:

- the right column reads `/line` again on every event and puts the answer
  in its place (`hx-trigger="sse:message"`), and so does `/line`'s own copy
  of it. A bead that is held shows there with its reason as soon as the
  event arrives, and goes when it is unheld. Home's column reads
  `/line?home=1` instead, so an event never brings the waits back beside
  the centre that lists them;
- `/sessions`, `/crew`, `/beads`, a bead's page, the talk page's newest
  letters and the depot, group, route, peer, edge and quota lists read
  themselves again the same way;
- on `/flows` each card and each section listens for the message named
  after it ([Flows](#flows));
- the yard panel (`/flows?panel=1`) reads itself again on every event, as
  `/line` does ([The yard panel](#the-yard-panel));
- Home's feed takes each kept event's fragment on top ([The stream](#the-stream)).

While one such read is under way, htmx keeps only the last of the events
that arrive, so a burst costs two reads and not one each. What changes
with no event is as old as the last one: an age (`3h 4m`), and a
crew member's unread count after it acknowledged its inbox, which the yard
does not log.

One stream per page, because a browser allows a host six connections
(HTTP/1.1) over all its tabs: a seventh page of the yard waits for one.
The server limits them too, far above that ([The stream](#the-stream)).

## The feed

Home shows what happened in the yard, one line per event, the newest on
top: what happened, the bead, its title and when. It keeps ten lines,
what a person wants on a phone, and `All activity` carries the rest to
the full feed: `?all=1` renders the latest 50 entries as the feed always
did, and the link under them (`Older`) is the next page, which htmx
follows by itself when it scrolls into view, so scrolling down goes back
in time. Without scripts they are plain links. The JSON keeps every
entry either way.

Home keeps a hold and its lifting; a session blocked on a prompt and its
return; a failure (a builder that gave up, a harness error, a session
that ended badly, a crew that stays stopped); a landing or a close; a
move into or out of a stage a person works, or back on `failed` or
`changes`; a note by a user or a crew member, a release, a reopen, a
firing timer. A landing stands once: its move to the terminal stage is
hidden beside it. A note by the bead's own agent (`<bead>-<stage>`) or
by a script group (an exec session acts as its group, and the landing is
one) stands behind `All activity` with the rest: builders' noted lines
were most of the feed.

The rest stands behind `All activity`, whole: creations, claims, starts,
updates, dependencies, subscriptions, crew turns and starts and stops,
prompts and plumbing, an agent's or the landing's note, and every
routine move. A bead's page keeps every event of the bead, none left
out, and `/events?format=json` keeps every event for machines.

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
  manual group; `no route: waits for a person` when no route matches); for
  a route with further groups all of them in order (`builders, else
  spare-builders`), since which takes a bead is chosen when it is claimed
  ([groups.md](groups.md#a-route-with-further-groups-and-quota-conditions)),
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
  "group", "train", "updated_at"}`, with `eta` and `late` beside them on
  the pages that read estimates (`/flows`, `/beads`, a bead's page), when
  the bead has one to show
  ([Estimates](#estimates-the-eta-and-the-late-mark)): `held` is a bead on
  hold, `working` one a session has claimed. On the page a late bead's
  card carries `⚠ late`, with the mark's whole line as its title.
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
a signal panel: one line plan per flow — one thin track line, each stage a
section of it with its name and count under it, a manual stage as a short
siding above, the other edges as thin unlabelled lines — with the open
beads standing on it as lights. A flow with no open bead still draws its
bare track. The page has no right column, so its centre runs to the
window's edge: each depot as wide as its widest flow plan needs, with
depots side by side where there is room. On a phone they stack, and a
diagram wider than its depot pans horizontally at full size.

How to read it:

- A light is its bead: tapping it opens the bead. What it is stands in its
  title; a blue light's title names what holds it. Lights stand newest
  first; past ten on one section the rest fold into `+N`, and the count
  beside the stage name always says the true number. A stage with none
  carries no count.
- A section is lit green while a session runs in it. No bead writes
  anything on the plan: no id, no group name, nothing beside its light.
- Every blocked kind is told apart: solid red for held, hollow red for
  gave up, hollow green for stalled, green with a red ring for blocked on
  a prompt, blue for waiting on a dependency, a red diamond before a bead
  back on `failed` or `changes`. A bead running past its usual time gets
  an amber ring around its light besides, its title naming the stage age
  against the p90; late stands beside the stalled lamp, never instead of
  it ([Estimates](#estimates-the-eta-and-the-late-mark)).
- A stage that waits for a person carries a lamp: red where the wait is a
  fault (held, blocked, gave up), blue where it is a person's turn by
  design (a manual stage, or an open ask to the operator). A stage that can report `changes` carries a
  signal instead: a lamp on a short post, an unlit outline unless the
  stage waits, when it lights red or blue as a wait lamp does; a wait on
  a stage with no signal is a bare lamp. A bead back on `failed` or
  `changes` lights no lamp: its diamond is its only mark. A stage no
  route serves is dotted with the faint words `no route`.
- Tapping a section opens the stage on `/flows`, at its section.
- Under a depot's diagrams, `off the line: N` holds the open beads in no
  stage of its flows, each with its stage named: a bead stranded in a
  stage its flow lacks stands there, since no section can hold it.

The whole panel reads itself again on every event of the stream
([Live pages](#live-pages)): a bead that moves is on its new section
without a reload. The parameter shapes the HTML alone: as JSON the page
answers the same with it as without it. Every colour the panel uses is a
named token of the stylesheet (`--panel-*` for the track, with `--running`,
`--held` and `--accent` for lit green, fault red and person's-turn blue,
and `--late` for the late ring), so the palette changes in one edit.

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
name the cards of the other pages have. Beside them are the bead's
`eta` and `late`, absent when there is nothing to say
([Estimates](#estimates-the-eta-and-the-late-mark)); `bead list --json`
itself carries no estimate, so these two are the page's, following the
card.

`depots` and `stages` are what the filters can be set to: the yard's depots,
and the stages that hold a listed bead (of `depot`, when it is set). Journals
and memories are not work and not listed, as in `yardr bead list`.

The page is live by reading itself again: its stream is the shell's, with
`format=seq`, and it asks for its own URL at each event, one request at a
time.

## One bead

`/beads/<id>` is what `yardr prime --bead <id>` knows, for a person: title,
type, stage, status and priority; under the header, the ETA sentence and
the late mark when there is anything to say
([Estimates](#estimates-the-eta-and-the-late-mark)); its depot, the flow that governs it and
where that comes from, its base, its train, the bead it was discovered from;
the open beads that block it; a train's children; the beads it led to; the
body; the stage file of its stage (without its
`summary:` line, in the page; whole in the JSON); the outcomes the
stage offers, each with the command that reports it (the next stages, for a
stage with no outcome); its edges; its board, the note pages in note order
where there are any; its notes, the oldest first, each with
its images and pages below its words; the sessions that worked it; its
history. A closed bead has a page too. An unknown id is a 404.

On a view that acts, the page answers the bead's open asks to its user,
oldest first: the question, who asked, the context, the
options numbered with what each does, the recommendation, and for a secret
what it asks for, for a run the command with what it changes and the way
back. Each answers under itself, with its options as buttons, with Later
and Decline; a secret is given, and a run approved, with `yardr bead
answer` beside, as the page says. Where the bead waits for the user in a
stage a manual group works, the outcomes send it on as `yardr review`
offers them, with a note. Each write carries the revision the page was
drawn from ([Acting as a user](#acting-as-a-user)).

A note's images and pages are what `yardr bead note --file` attached to
it. Each image shows below the note's words, with those words as its alt
text; each page stands as a link, `page <hash8>`, opening the bytes
top-level and inert. One the yard no longer holds is a line saying so,
never a broken icon or link. The JSON names them as the notes have them.
Above the notes, the board lays the bead's note pages out in note order,
each with its note beside it: one entry per page, the link first and the
note's words under it. A bead with no note page shows no board. Mail
pages stand on no board: only a note's page opens.
The bytes come from `/blobs/<hash>`, which serves only blobs a note on
some bead names: any other hash, a mail's file included, answers as a
missing blob, since the view has no login.

A page opens under its own policy, which replaces the yard page's for
that response: `sandbox` with no `allow-scripts`, so scripts, forms and
plugins are dead, and a fetch allowlist of `data:` images and inline
styles only, so nothing loads over the network and no frame loads or
embeds the page. The sandbox's opaque origin withholds the yard from the
page; what survives is the reader following a link out by clicking it.
The yard's own pages keep their policy, and the no-other-host test still
holds: a navigation is not a fetch.

Each page carries run beside its static link, opening the same bytes on
the sandbox origin: the view's second listener, on loopback beside the
view ([The address](#the-address)), answering blob bytes only and no
yard route. A reader on this machine goes straight to where the origin
listens; a reader elsewhere goes by the name the view was reached by,
on the origin's port, or by the origin's public name where `--pages-host`
gives one ([Behind a proxy](#behind-a-proxy)).

A page run there runs its inline scripts under the walls that hold:
no cookies, no reach into the yard (`connect-src 'none'` stops fetch,
XHR, WebSocket and EventSource), no form posts (`form-action 'none'`),
no popups, and blob bytes only — the origin answers no yard route. The
sandbox flags stay as they are: with no `allow-popups` a script cannot
open a new window, and with no `allow-top-navigation` a nested page
cannot navigate its top frame. What a script can carry out is the
page's own words and what a reader types into it: by navigating away
with them in a URL — a page opened top-level, as run opens it, can
always navigate itself by script, by meta refresh, by its reader's
click, as any web page can — and by WebRTC, which no released browser
blocks (the policy carries the `webrtc 'block'` directive, but as of
October 2026 no released browser enforces it; probed in HeadlessChrome
155 with the exact headers on 14 Oct 2026, where fetch, workers,
frames, popups, cookies and storage were all blocked). A reader who
types into a page from a note trusts its author.

```json
{
  "bead": {"id": "yardr-822r", "title": "web: flows and beads, live", "body": "Child 3 of train …", "type": "task",
           "stage": "new", "status": "open", "priority": 2, "depot": "yardr", "labels": [], "group": "yardr-builders",
           "session": "vf0dsef5y990pzq0ybnh", "created_at": "…", "updated_at": "…", "revision": "…"},
  "eta": {"low": "2h", "high": "5h", "queue": "1h", "ahead": 3,
          "stages": [{"stage": "new", "group": "muse-builders", "median": "30m"},
                     {"stage": "review", "group": "muse-builders", "median": "20m"}],
          "unclocked": ["approved"], "samples": 42,
          "line": "eta usual 2h–5h (queue ~1h behind 3 beads for muse-builders; new ~30m; review ~20m; approved after your decision)"},
  "late": {"stage": "new", "clock": "waiting", "in_stage": "6h", "p90": "2h",
           "line": "late: waiting in new 6h, past the usual 2h (p90 entry-to-exit for difficulty:2 in muse-builders)"},
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
- `eta` and `late` are the bead's estimate, absent when there is nothing
  to say ([Estimates](#estimates-the-eta-and-the-late-mark)); the page's
  cards carry their beads' too.
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

## Estimates: the ETA and the late mark

Every open bead with enough history behind it carries an estimate: its
ETA as a usual range, and a late mark when its stage has run past its
slow time. The bead's page shows the ETA sentence and the mark under
the header; cards carry the mark (`⚠ late`, with the whole line as its
title); the yard panel rings a late light amber; `/line` lists late
beads under the waits. `yardr bead show` and `yardr prime --bead` print
the same two lines, and `bead show --json` carries the same two
objects. The socket's `Bead` call carries them too, so footplate reads
what the view shows.

The ETA counts the queue wait plus each machine stage ahead, timed for
its primary group — the first group of the route that would take the
bead there, since the quota's choice is made at the claim and never
predicted. The low end sums the stretched claim-to-exit medians, the
high end the stretched p90s: a component-wise usual, never a promise.
What it leaves out it names: people's stages (`after your decision`),
stages below 10 durations (`too new to time`), the yard's cap (`behind
the yard's cap`), and wherever the walk down the flow's `done` outcomes
stops (`then …`, `unclocked after …`). Below the floor everywhere, and
with no machine stage ahead, both objects stay absent. The queue itself
is a slot simulation over the order the dispatcher claims in
([line.md](line.md#how-long-the-line-is-the-eta)).

The cells behind them are kept as stages end, per group, `difficulty:`
label and stage
([groups.md](groups.md#what-a-groups-beads-cost-the-estimate-cells)); a
yard from before them replays its retained log once at upgrade, and its
open stays start their clocks then: a waiting bead counts its stage age
from the upgrade, while a running bead's clock is its live session's
start.

Late is past the stage's slow time: waiting past the whole-stay p90, or
running past the claimed-run p90. Stalled means the agent stopped —
its first prompt idle past the stall limit, a hollow green lamp on the
panel; late means it takes long. A bead can be either, both, or
neither, and late tells no one: there is no event for it, only the
mark.

The objects, as `eta` and `late` on the bead's page, on the cards of
`/flows` and the rows of `/beads`, and on `/line`'s late rows (`late`
always set there, `eta` beside it when there is more to say):

```json
"eta": {"low": "2h", "high": "5h", "queue": "1h", "ahead": 3,
        "stages": [{"stage": "new", "group": "muse-builders", "median": "30m"},
                   {"stage": "review", "group": "muse-builders", "median": "20m"}],
        "unclocked": ["approved"], "samples": 42,
        "line": "eta usual 2h–5h (queue ~1h behind 3 beads for muse-builders; new ~30m; review ~20m; approved after your decision)"},
"late": {"stage": "new", "clock": "waiting", "in_stage": "6h", "p90": "2h",
         "line": "late: waiting in new 6h, past the usual 2h (p90 entry-to-exit for difficulty:2 in muse-builders)"}
```

- `low` and `high` are the range ends; `queue` with `ahead` the wait
  and the beads ahead of it, absent when the wait is zero or
  unnumbered.
- `stages` names each clocked machine stage with its primary group and
  stretched median; `unclocked` the stages named but not timed.
- `samples` is the smallest cell count behind the range.
- `late`'s `clock` is `waiting` or `running`, `in_stage` the age
  against the p90.
- `line` is the sentence as the bead's page and `bead show` print it.

## Talk

On a view that acts, `/talk` is its user's conversation
with a crew member — the web's `yardr chat` for one member at a time — with
the form that sends it mail as them. `?with=` names the member; without it
the page opens on the yardmaster, else on the first member. The crew stand
above the letters, each linking to its conversation, with a dot where the
user has unread mail from it. Each letter reads as a note does: the sender
with its device, the time, the words with their lines kept, the images it
carries, and `read` under the user's own where the member acknowledged it.
A page the mail carries is named without a link, never drawn as an image:
only a note's page opens. With no user the page says the view does not
act.

The form posts to `/talk?with=<member>` ([Acting as a user](#acting-as-a-user)):
mail sent, the page lands back on the conversation; mail refused stays in
the form with the reason. The newest 50 letters are live: each event of
the yard reads them again, so an answer shows without a reload. Past them
an "Older mail" link reads the next 50 under `?before=`, a snapshot read
undisturbed, with the way back beside it.

As JSON:

```json
{
  "as": "mckean",
  "with": "yardmaster",
  "crew": [
    {"name": "yardmaster", "unread": true},
    {"name": "scribe", "unread": false}
  ],
  "letters": [
    {"seq": 13, "at": "2026-10-08T09:30:00Z", "from": "mckean", "to": "yardmaster",
     "text": "Paint the shed.", "read": true, "own": true, "sender": "mckean"},
    {"seq": 35, "at": "2026-10-08T09:30:00Z", "from": "yardmaster", "to": "mckean",
     "text": "It is painted.", "read": false, "own": false, "sender": "yardmaster"}
  ],
  "head": 35,
  "max_mail": 4096
}
```

`letters` are the socket's [`Conversation`](api.md#conversation) newest
page, each with `own` (whether the view's user wrote it) and `sender` (who
the page names for it) beside them, and `more` beside them where older mail
was left out. `error` and `draft` stand only on a POST that sent nothing:
why, and the words back in the form.

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
- With `?format=home` the `data` is the entry as above, but only for an
  event home's feed keeps; every other is sent as its `id` with no `data`,
  as an event the feed leaves out is. Home hangs its feed on it, and
  `/?all=1` on the unfiltered stream.
- With `?format=json`, or an `Accept` that prefers JSON, the `data` is the
  event itself, as `yardr watch --json` prints it
  (`{"seq", "id", "bead", "kind", "actor", "data", "at"}`), and no event is
  left out. `EventSource` cannot set `Accept`; the parameter is for it.
- With `?format=seq` the `data` is the event's seq and nothing else, for a
  page that reads itself again when something happens (`/beads`, a bead's
  page, the depot, group, route, peer, edge and quota lists). No event is
  left out.
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

## Health

`/health` is the yard's liveness facts for a monitor, a proxy or
footplate: the server's build and start time, whether the daemon holds
its lock, on which build, and when it last completed a pass, and the
view's own build. It is a machine route: JSON to every client, with no
template and no menu entry.

```sh
curl -s http://127.0.0.1:8791/health
```

```json
{
  "status": "ok",
  "server": {"build": "1904417", "since": "2026-10-14T09:02:11Z"},
  "daemon": {"running": true, "build": "1904417", "last_pass": "2026-10-14T09:01:44Z"},
  "view": {"build": "1904417"}
}
```

It answers 200 whenever the view serves it. `status` is `ok` when the
yard is whole — the server answers, the daemon holds its lock, its
build is on record and it stamped a pass — and `degraded` otherwise: a
proxy keeps routing to a view that answers, even over a yard whose
daemon is down, and the JSON says whether the yard is whole. A part
with nothing on record is left out, not guessed at: a daemon from
before its starts were recorded has no `build`, one that never stamped
a pass has no `last_pass`. Only a yard it cannot read at all fails,
with a 500 through the error path. Nothing it says is a secret, and it
is served to the same audience as the rest of the view; the store's
head sequence is deliberately not among it.

## Layout

Three columns on a desktop, the centre fluid. Simplicity is the point:
**a new view is a new feed in the centre column, never a new
arrangement.**

- **Left**, 200px: the yard's name and a short menu (Home, Yard, Flows,
  Beads, Sessions, Crew members, Config).
- **Centre**: the page. At 1100px and wider it takes all the width the
  menu and the aside leave, so every page fills the window and the menu
  and the aside stand in the same place on every page that has them.
- **Right**, 300px: what waits for a person, and a summary of the line. It
  is the same on every page but home, whose aside is the line alone.

A wide centre stays readable: a list's rows keep their one line, with the
time at the right edge; running text (a bead's body, a note: `.text`,
`.entry-note`) keeps a measure of about 90 characters; name and number
pairs (`.figures`, `.rows`, `.facts`) use the width.

The page scrolls as a document: no column has a scrollbar of its own, at
any width. The menu stays in view while the page scrolls under it; the
right column scrolls with the page. Wide text that must not wrap (a stage
file, `.text.file`) still scrolls sideways inside its block.

| window | arrangement |
|---|---|
| 1100px and wider | menu, a fluid centre, right |
| 700px to 1099px | menu and centre, up to 600px and centred; the right column goes, and a Line entry in the menu leads to `/line` |
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
| `--shade` | `rgba(0, 0, 0, 0.45)` | the same | the backdrop that dims the page under the phone's More sheet |
| `--working` | the accent | the accent | status: a session is working |
| `--running` | `#6db58a` | `#2f7d55` | status: idle or running |
| `--held` | `#e0735f` | `#b3402c` | status: blocked or held, and errors |
| `--done` | faint | faint | status: done |
| `--panel-board` | `#202622` | `#f5f1e6` | the panel's track: its board |
| `--panel-ink` | `#e6e1d3` | `#1d2420` | the panel's track: stage names |
| `--panel-dim` | `#9aa39b` | `#5d665f` | the panel's track: faint names, unlit lamps |
| `--panel-line` | `#d8d2c0` | `#2a2f2b` | the panel's track: the lines |
| `--panel-platform` | `#3a423c` | `#cfc7b2` | the panel's track: quiet lights |
| `--mono` | | | the main face: `"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, monospace` |
| `--sans` | | | human words — home's answer, reasons and titles — and longer prose: `"Inter", system-ui, sans-serif` |
| `--text`, `--small`, `--large` | | | 14px, 11px (labels), 20px (names and figures; on home the answer line alone) |
| `--gap` | | | 16px, the one spacing unit |
| `--label-spacing` | | | 0.14em, for uppercase labels |
| `--menu-width`, `--centre-width`, `--right-width` | | | the three columns |

The light theme applies under `prefers-color-scheme: light`. The fonts are
the reader's own: none is downloaded, so a machine without JetBrains Mono
or Inter shows its system monospace and sans.

Rules the stylesheet holds to, and a test checks the second:

- Small uppercase labels with wide letter-spacing (`.label`) name things;
  values are in the plain face.
- Weight: one large line per screen (`.answer` on home, `--large`);
  human words (titles, reasons, the answer) in `--sans`, machine words
  (ids, counts, times, stages) monospace; every row and feed line one
  line, ending in an ellipsis.
- Corners are square everywhere (`border-radius: 0`). The one circle is a
  status dot (`.dot`, with `.working`, `.running` or `.held`).
- Words come from the yard: line, landed, held, waits for you.

Classes a page builds from: `.item` (one entry of the centre feed, with a
hairline under it), `.item-head`, `.label`, `.panel`, `.figures` and
`.rows` (name and number pairs), `.lines` (one line per row, as the home
page's overview shows them), `.answer` (home's answer line, the screen's
one large line), `.feed` and `.entry` (the feed and one event
of it, with `.entry-head`, `.entry-what`, `.entry-title`, `.entry-note`),
`.wait` (a bead that waits, with `.wait-title` and `.why`; dimmed inside
`.parked`), `.card` (one bead in a list, the `card`
template, with `.card-head`, `.card-title`, and `.arrived` for one the
stream brought), `.cards` (a list of them), `.column` and `.stage` (a flow
and one stage of it), `.peers` (the crew to talk to), `.letter` (one mail
of the talk page, as a `.note` reads), `.error` (why a mail was not sent),
`.filters`, `.facts` (name and value rows), `.routes` (one row per route, as `/routes` shows them on a wide window), `.text`
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
