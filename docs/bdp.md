# BDP (Bead Protocol)

`yardr bdp serve` serves the yard over the [BDP](https://github.com/gastownhall/bdp)
Read profile, so any BDP client reads yardr's beads, dependency links and
links to outside issues over HTTP:

```sh
yardr bdp serve --scope https://beads.example/team/
```

`yardr bdp start` runs it in the background with `--addr`, `--scope` and
`--host`, logging to `<home>/bdp.log`, `yardr bdp stop` stops it, and
`yardr bdp install` makes it a login service
([configuration.md](configuration.md#long-running-commands)).

All three listen on `--addr` when it is given, else on `YARDR_BDP_ADDR` when
set in the command's environment, else on the yard's `bdp_addr` setting
(`yardr settings set bdp_addr 127.0.0.1:8766`), else on `127.0.0.1:8765`
([configuration.md](configuration.md#settings)), on loopback only
([below](#loopback-only)).

## Loopback only

There is no authentication: whoever reaches the server reads every bead's
title, body and metadata. So, as the web view does ([web.md](web.md#loopback-only)),
the server listens on the loopback interface and nowhere else unless
`--unsafe` is given, and answers only to the names it is known by:

- `--addr` takes `127.0.0.1`, `::1` or `localhost` with a port. Any other
  address, and one with no host (`:8765` listens on every interface), is
  refused with a message that says why: before the yard is touched when the
  flag gives it, once the yard is read when `YARDR_BDP_ADDR` or the `bdp_addr`
  setting does (the setting takes a loopback address too; one a yard
  already holds is refused once the yard is read).
- A request whose `Host` header is not a loopback name (`localhost`,
  `127.0.0.1`, `[::1]`), the Scope URL's host or a name given with `--host`
  is answered 421 with no body. A page on another site can otherwise point a
  name of its own at 127.0.0.1 and read the yard through the visitor's
  browser (DNS rebinding). The Scope URL's host is the name clients reach the
  server by, so behind a proxy that forwards `Host` unchanged,
  `--scope https://beads.example/team/` is all it needs. A request target in
  absolute form (`GET http://other.example/team/bdp.json`) names an authority
  of its own, which BDP ignores (the Scope decides identity) and a browser
  sends to a proxy only: it is served whatever the authority is.

`--unsafe` listens on every interface (`0.0.0.0:8765`, on the selected
address's port, or the `--addr` given, which may then be any address), to read
the yard from another machine on the same network. There is still no
authentication: anyone who can reach this machine on that port reads the
whole yard. A request is then answered when its `Host` is an IP address, a
loopback name, the Scope URL's host, this machine's host name (with or
without `.local`) or a name given with `--host`, and 421 under any other
name. The default Scope names the address that was selected
(`http://127.0.0.1:8765/yard/`), which `--unsafe` does not change: give
`--scope` the name clients reach the server by.

`--host <name>` names a host the machine is reached by and cannot work out by
itself: its name in the LAN's DNS, on a tailnet, or a proxy's. It is answered
as the machine's own names are, in any case and with any port. It takes one
name or a comma list and may be repeated; a wildcard, an empty name and a name
with a scheme or a port are refused, and so is an IP address off loopback
without `--unsafe`. Without `--unsafe` it leaves the server on loopback, for a
proxy of yours on this machine that forwards the `Host` header unchanged; what
to ask of one, and why to name only names whose resolution you control, is
[web.md](web.md#behind-a-proxy)'s.

`yardr bdp start` and `yardr bdp install` take `--host` and not `--unsafe`: a
server no terminal runs stays on loopback, and an address off it is refused
before anything is started or written, as the web view's are. `yardr up`
refuses a `bdp_addr` off loopback the same way, and starts nothing for it.

- Beads are `<scope>beads/<handle>`; dependencies are Links whose source is
  the waiting bead and target its blocker, as in bd. The handle is the BDP
  id because it never changes and is unique within a yard (BDP ids are
  scoped), and it is what bd and humans use; the UUID is the `uuid`
  property. Listings follow BDP's canonical id order, not creation order.
- Bead properties are bd's JSON row (dependencies, counts, `created_by`, …)
  plus yardr's own fields; a held bead reads as `deferred`. Types use BDP's
  reference vocabulary (`https://work.example/types/task`, `…/blocks`) where
  names match, else `<scope>types/<name>`. A train is published as BDP's
  and bd's epic, in its Type (`https://work.example/types/epic`) and in
  `issue_type` (`"epic"`).
- A `discovered-from` edge (`yardr bead create --discovered-from <origin>`)
  is a Link of Type `<scope>types/discovered-from`: BDP's reference
  vocabulary has none. Its source is the found bead and its target the
  origin, as bd's `discovered-from` dependency, and the found bead's
  `dependencies` row lists it with `"type": "discovered-from"`. The Type
  conforms to nothing, so no client reads it as a blocker.
- Every real change mints an opaque revision (a no-op mints none), served
  as the ETag; listings page over one consistent snapshot.
- A record names the actor of its last change in `revisionProvenance`
  (`actor.id`, with `basis` `writer-supplied`: the name the writer gave,
  which nothing authenticates). BDP's common `metadata` is always `{}`: a
  bead's own metadata is a field of bd's row and stays in `properties`.
- The revision is the compare-and-swap token a BDP write profile will take
  (`If-Match`). It is the bead's `revision` in `yardr bead show --json`, and
  the store already enforces it: `bead update`, `hold`/`unhold`, `advance`,
  `done` and `close` take `--revision <rev>` (in Go, `store.Expect.Revision`)
  and refuse with a `stale` conflict, changing nothing, when the bead has
  changed since that revision was read. Crew members carry their own `revision`
  (`yardr crew show --json`) for `crew add`, `start`, `stop`,
  `restart` and `rm`.
- BDP's own conformance suite passes against it (31/31 runnable Read
  scenarios on its main caf5ca7, 13 Oct 2026), and BDP's `bdp bd ready`
  client computes the same ready set from yardr as `yardr ready`. See
  [conformance/bdp](../conformance/bdp).

## Links to outside issues

A bead's link to a Linear or GitHub issue ([links.md](links.md)) is served as
a Link of its own, `<scope>links/<link-id>`:

- `source` is the bead, `target` the issue's URL in its fixed form. The
  target is an out-of-Scope endpoint: opaque, never dereferenced, compared
  as given, which is why yardr keeps one form per issue.
- `properties` has `source` (`linear`, `github`) and, when a delivery gave
  it, `thread`: the issue's thread id in that source.
- The Type is `<scope>types/relates`, or `<scope>types/tracks-<source>`
  (`tracks-linear`, `tracks-github`), which conforms to `<scope>types/tracks`.
  `links/?conformsTo=<scope>types/tracks` lists every tracked issue;
  `links/?target=<issue URL>` lists an issue's beads. The Types are
  Scope-local and resolve whether or not the yard has such a link yet.
  `relates` is not BDP's reference `relates`: its target is no work item.
- A link has its own revision. Its kind is fixed, as BDP requires of a
  Link's type: replacing a bead's `relates` link with a `tracks` one, or
  moving the tracker, deletes Links and creates new ones.
- Discovery (`bdp.json`) advertises `maximumEndpointMultiplicity`: for each
  `tracks-<source>` Type, endpoint `source`, max 1. A bead tracks at most one
  issue per source.
- That an issue has at most one tracking bead is yardr's own rule, kept by
  the store and **not** advertised: BDP v0 never counts or constrains an
  out-of-Scope endpoint.
- A journal's or memory's links are not served, as the bead is not.
