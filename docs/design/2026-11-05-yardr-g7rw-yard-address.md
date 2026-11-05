# One address for a yard: mail, peering, BDP scope and provenance

Design yardr-g7rw, proposed 2026-11-05. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead.

## 1. The question

mckean (mail 114243, 29 Oct): "yards getting private addresses makes sense
generally for peering, addressing, and also provenance, I think even the bead
definition supports this." The context is an inbound-mail thread where
yardmaster first proposed a per-yard forwarding address at the edge
(`<yard>.<random>@in.yardr.dev`) for hosted yards; mckean rejected the edge
doing users' mail (yardr-adza is the yard doing it in their own account) but
kept the per-yard address as a general idea. The question is whether every yard
should have one stable address of its own, and what it would be used for.

Today a yard has several unrelated names: the edge hook path
(`my.yardr.dev/hooks/<user>/<machine>/...`), peer names local to each linking
yard (`crew@<peer>`), the BDP scope a person picks by flag (`yardr bdp serve
--scope`), and `revisionProvenance` whose basis is `writer-supplied` (nothing
authenticates it, [bdp.md](../bdp.md)). This document decides, in order: (1)
the address's form and issuer; (2) each of the four uses — mail intake, peering
by address, BDP scope, verifiable provenance — kept or dropped with the fact
that settles it; (3) the self-hosted case with no edge; (4) what is not worth
doing, named. It then lists the build beads. Building any of it is left out.

Terms. A *yard* is one installation of yardr: its store, its crew and its
depots ([glossary.md](../glossary.md)). The *edge* is the service at
`https://my.yardr.dev` that stands in front of yards ([edge.md](../edge.md)).
A *peer* is another yard this one is linked with; a *link* is the stored
peering, keyed by the other yard's public key. BDP is the Bead Protocol Read
profile yardr serves with `yardr bdp serve` ([bdp.md](../bdp.md)); its *Scope*
is the canonical URL every bead and link id is minted under. The yard's
principles apply: generalisation, composability and simplicity — prefer a
mechanism that exists over a new one, and a general one over a special case.

## 2. What exists today

### 2.1. The yard already has a key, and nothing else stable

Each yard has one ed25519 key pair, made once at `~/.yardr/peer.key`
(`config.PeerKey` in [internal/config/config.go](../../internal/config/config.go);
`yardkey.Ensure` in [internal/yardkey/yardkey.go](../../internal/yardkey/yardkey.go),
mode 0600, no rotation). That key signs peer requests and answers
(`SignRequest`/`VerifyRequest`, labels `yardr-peer-request-1`), the edge link
hello (`SignLink`) and nothing else needs a second identity: the key *is* the
yard to every yard it is linked with. The stored peer record,
`store.Peer` in [internal/store/peers.go](../../internal/store/peers.go), is
keyed by that public key; the peer's name beside it is this yard's own label
(`peer rename` changes it and the other yard hears nothing, [peers.md](../peers.md)).
There is no directory, no lookup, and no name a third yard could type. The
key is the yard's *identity*: the yard makes it itself and every link pins
it. It is not a *locator*: nothing in the key says where the yard can be
reached, and no address below is derived from it (§3.1 keeps the two apart).

### 2.2. The edge already gives a connected yard a two-part name

`yardr edge connect` enrols a yard by its key and returns an account handle
and a yard name ([edge.md](../edge.md), "Connecting a yard"); the `edge`
setting keeps both. Who issues that name matters for §3.1: the yard only
*proposes* one (its own name, or `--as <name>`); the person approving on the
edge's `/connect` page sees the proposal and may change it, and what the
edge answers back — handle and name — is what is kept
([internal/cli/edge.go](../../internal/cli/edge.go), the connect command's
long text and the write of `store.Edge{Handle: en.Handle, Name: en.Name}`).
Every public name of the yard is then derived:
`https://my.yardr.dev/hooks/<account>/<yard>/<hook>` for webhooks and
`https://my.yardr.dev/peers/<account>/<yard>` for peer calls. The yard dials
out and keeps one link open; the edge forwards and keeps nothing, and reads
everything it forwards in the clear ("What the edge reads, and what it never
holds"). A yard with no edge has none of these names.

The design for peering on standards (yardr-4687, decided 2026-10-26,
[2026-10-26-yardr-4687-peering-on-standards.md](2026-10-26-yardr-4687-peering-on-standards.md))
already proposes the lookup half of an address: the edge name with the
protocol segment dropped, `https://my.yardr.dev/<account>/<yard>`, as a
WebFinger (RFC 7033) resource whose record carries the peer endpoint, the
signing key and a new encryption key; HTTP Message Signatures (RFC 9421) in
place of the bespoke signature; and HPKE-sealed bodies. That design is the
substrate here, not a rival: this document decides whether one address should
also carry mail, BDP scope and provenance, which yardr-4687 left out.

### 2.3. Mail intake today is a hook the yard runs itself

Inbound mail reaches a yard as a hook delivery, not as mail to the yard.
The `cloudflare-email` pack (yardr-packs) routes its one fixed address,
`yardmaster@yardr.dev`, through
Cloudflare Email Routing to a Worker, which signs each message and posts it
to the yard's hook URL; `yardr hook add cloudflare-email` verifies it, an
unmapped sender is dropped with a mapping card, and a subscription files it
in the yardmaster's inbox. The `cloudflare` pack in `packs/cloudflare` gives
the yard the Email Routing reads and writes to set that up, each write on
its own card. yardr-adza settled the direction: the yard does mail in the
operator's own account. The edge hosts no mailbox, holds no mail secret, and
queues only webhook deliveries while a yard is away, bounded and for seven
days ([edge.md](../edge.md), "While the yard is away: the queue"). Mail
*between* yards is a different thing and already exists: `yardr mail
<name>@<peer>` over a link, to a name on the peer's roster ([peers.md](../peers.md)).

### 2.4. Linking today needs the two operators in contact

Two ways make the same stored link ([peers.md](../peers.md), "Linking two
yards"): pairing (`yardr peer pair`, an eight-character code read to the
other operator, a six-digit check over both names, keys, URL and nonces,
commit-then-reveal; [internal/pairing/pairing.go](../../internal/pairing/pairing.go),
[internal/store/pairing_messages.go](../../internal/store/pairing_messages.go)),
and invite lines by hand (`yardr-peer-1,<name>,<key>[,<url>]`, `yardkey.Invite`).
The two differ in how the key arrives. In a pairing the public keys cross
inside the protocol exchange itself (each side's `Party` in the transcript);
what the operators pass out of band is only the short code, read aloud, and
the six-digit check they compare. An invite line carries the key in the line
an operator carries by hand. Neither gives a stranger a name to look up.
The six-digit check is the link's root of trust, and `--yes` gives it up.

### 2.5. BDP scope today is a flag, and provenance authenticates nothing

`yardr bdp serve --scope` takes any URL ending in `/`; with none, the scope
is `http://<addr>/yard/` ([internal/cli/bdp.go](../../internal/cli/bdp.go)).
Every bead id is `<scope>beads/<handle>`, every type `<scope>types/<name>`.
The scope is therefore already a URL a person chooses per yard, with no tie
to the yard's key or edge name, and two yards may mint colliding ids by
picking the same scope. Provenance is weaker still: `provenance()` in
[internal/bdp/graph.go](../../internal/bdp/graph.go) writes
`revisionProvenance.actor = {id: <the --actor name>, basis: "writer-supplied"}`,
and its comment says the name is the one the writer gave, which nothing
authenticates. The store does keep what a signature would need: each bead's
revision is the compare-and-swap token, and every write names its actor.

Two further facts settle two of the four uses, so they stand here. First,
BDP is not served at an edge address and is not public reading today. The
edge carries webhooks, peer and device calls, the web view and previews
([edge.md](../edge.md), opening); the yard's own listener serves `/hooks/`,
`/peers/` and `/user/` and says so plainly — "BDP and the rest of the socket
are not on it" ([internal/server/server.go](../../internal/server/server.go),
`serveHooks`). `yardr bdp serve` is a separate server with no
authentication that listens on loopback unless `--unsafe` is given
([internal/cli/bdp.go](../../internal/cli/bdp.go); [bdp.md](../bdp.md),
"Loopback only"): whoever reaches it reads every bead's title, body and
metadata. Second, BDP v0's schema is closed. In
`bdp-v0.schema.json` (github.com/gastownhall/bdp,
`specs/profiles/core/`) the bead and link records, `revisionProvenance`
and `revisionActor` all carry `additionalProperties: false`;
`revisionActor` permits only `id` and `basis`, and `basis` only
`writer-supplied` or `unknown`. A signature or a key inside a record or
its provenance is therefore not an additive yardr field: no spelling of
it validates under the profile yardr serves and passes conformance
against today. The open bags, bead `properties` and common `metadata`,
are the only places extra keys would pass, and yardr's mapping keeps
`metadata` always `{}` for a reason ([bdp.md](../bdp.md)): a provenance
fact hidden where no BDP client looks for it is not served provenance.

### 2.6. User identities are names in sources, not addresses

A user is a name and an inbox ([users.md](../users.md)); `yardr user identity
<name> --source <source> --id <id>` says who that person is in Linear or
GitHub, and `yardr peer identity` maps a linked yard's crew member into a
source. Neither is an address of the yard, and neither should become one:
they name people, this address names the yard.

## 3. The proposal

### 3.1. (1) Form and issuer: an HTTPS URL as locator, the key as identity

**The yard's address is an HTTPS URL, and it is a locator, not the
identity. The identity stays the key; who issues the locator depends on
where it lives.** Keeping the two apart is the decision:

- *Identity* — the ed25519 public key (`peer.key`, §2.1). The yard makes
  it itself; no one else mints it. A link pins it, a signature is checked
  against it, and it survives every rename and reconnect. No address is
  derived from it and it is derived from no address.
- *Locator* — an HTTPS URL at which the yard's record (§3.2) can be
  fetched. The record names the identity; fetched over the host's TLS,
  through §3.2's guarded fetch, it is a claim, and it becomes the yard's
  address only once the key it names is pinned at first link exactly as
  a peer key is pinned today. A later record naming a different key is
  refused and shown, never followed (§3.2).

Who issues the locator, case by case:

| Yard | Locator | Issued and controlled by |
|---|---|---|
| Connected to the edge | `https://my.yardr.dev/<account>/<yard>` | The edge. The yard proposes a name (`--as`, else its own name); the approver may change it on the `/connect` page and the edge returns the handle and name that are kept (§2.2). The yard holds it only while connected; the edge's operator controls the host and its routing. A rename or a disconnection ends it, and the edge owes the old address no forwarding (Q5, decided, §5). A retired `<account>/<yard>` name is never reissued to another yard; the edge tombstones it, so no later yard answers at a path an existing link still stores (§3.5). The cost is that an operator cannot reclaim their own old name. |
| Its own domain | `https://<domain>/<path>` (any path) | The operator. They control the domain's DNS and whatever serves that host — the yard's listener or a proxy of theirs — and name the URL in the `yard_address` setting (§3.3, bead 1). Nothing is registered anywhere. |
| No edge, no domain | none | — The key, pairing and invite lines (§2.4) are the whole naming, as today. |

The edge form is the name the yard already has, with the `/hooks/` and
`/peers/` segments understood as doors at that address, not as names of
their own. The edge serves the address's record (§3.2) because enrolment
already gave it the account, the yard name and the key. There is no new
key, no registry and no certificate for the yard itself.

When a yard is both connected to the edge and has `yard_address` set,
it has two locators and may be found at either. The edge keeps serving
its record at the edge address for as long as the yard is connected and
findable, and the yard serves its record at `yard_address`; both
records name the same key, so a lookup at either pins the same yard.
Nothing carries `yard_address` to the edge and no edge change is
needed for this: `yard_address` is the self-hosted locator, the edge
address is the edge-issued one, and neither suppresses the other.
`yardr edge status` and `yardr doctor` print both where both stand.

This reuses what exists: the key (§2.1), the edge name (§2.2) and the
WebFinger record of yardr-4687 (§2.2). A mail address and a bare key were
both considered as the primary form and are left out (§4): the mail address
because mail is one use of four and an address that only receives mail
cannot be looked up for keys, and the bare key because no person types it
and no program resolves it without a scheme yardr would have to invent.

What a person sees:

```text
$ yardr peer key
a8tUnLBerkUE5hhF7C4ejE-GeV12GYuXpfNf1Jw1grY
$ yardr edge status
connected to https://my.yardr.dev as mckean/airy; this yard's address is https://my.yardr.dev/mckean/airy
```

`yardr edge status` and `yardr doctor` gain the one line; a yard with no
edge prints its key and no address until the operator names its domain
(§3.3; Q1, decided, §5).

### 3.2. (2) The four uses

**Discovery record (the mechanism the first two uses share).** Whoever hosts
the address's host answers `GET /.well-known/webfinger?resource=<address>`
with the yard's record: peer endpoint, signing key, and — once yardr-4687's
encryption bead lands — its encryption key. This is yardr-4687 §3.1
unchanged, including its findable opt-in (yardr-4687 Q4, decided): the
edge answers that lookup only for a yard whose operator turned findable
on, and answers 404 for every other yard — unknown, unfindable, and
retired (§3.1). A yard is not
published by being connected; findable is a publishing act, and a yard
that never asked to be found is not in the directory and cannot be
enumerated by guessing names. Findable is not set at enrolment alone:
the operator turns it on or off at any time with `yardr edge set
findable on|off`, carried over the yard's existing edge link, so
revoking findability — the privacy point of the opt-in — takes effect
at the edge without reconnecting (beads 2 and 4, §6; this extends
yardr-4687 bead 4, where findable is presented at enrolment only). A self-hosted yard serves a record only
where its operator serves one at `yard_address`, which is that yard's
own publishing act. A yard that fetches a record does so exactly as
yardr-4687 §3.4 specifies: through that design's one guarded client,
whose rules are part of the route here, not hardening to be added
later. In brief, with §3.4
for the full prefix table, quotas, timeouts and sizes: the looking-up
yard builds the target itself — the requested address must itself be an
`https` URL (no userinfo or fragment, port 443), its host is the lookup
host and nothing in any answer can move it, and the path is the fixed
`/.well-known/webfinger` with `resource=<the requested address>`; the
host must be a public fully qualified domain name (never an IP literal,
`localhost`, a single label, or a local suffix); every address it
resolves to is checked before anything is dialled against §3.4's
allowlist (globally routable unicast only, the lookup failing whole if
any answer is not), and a validated address is then dialled literally
with the original host as the TLS name, direct, with no proxy and no
second resolution; at most three redirects are followed, each only to
another `https` URL whose host passes the same name and address checks
before it is dialled. A record is accepted only when its `subject` is
exactly the requested address; any other subject is refused and nothing
is stored from it. The lookup's trust is then the host's TLS at first
sight, pinned by key afterwards; a later record that names a different
key is refused and shown, never followed.

The endpoint inside that record is untrusted in the same way, and it is
never stored or dialled merely because a record returned it. It passes
yardr-4687 §3.4's endpoint guard twice. *At storage:* an endpoint taken
from a record is refused — with the reason shown and nothing stored —
unless it parses as a peer URL parses today (`CleanURL` in
[internal/store/yardkey.go](../../internal/store/yardkey.go): no user,
query or fragment), its scheme is `https`, its host is a public fully
qualified domain name (never an IP literal), and every address that host
resolves to at that moment passes the same allowlist; an empty endpoint
(a yard that only pulls) needs no check and stores no URL, as today, and
the stored link remembers that its URL came from a lookup, so the dial
check below knows which URLs it owns. *At every dial:* every call to a
lookup-stored URL — a `peer ping`, a push, a pull, later dispatch —
resolves the host again, requires every answer to pass the same
allowlist again, and dials a validated address literally with the
original host as the TLS name, direct, with no proxy; a peer endpoint's
redirect is never followed, so a 3xx is a failed call, counted like any
unreachable answer. The lookup fetch's re-validated redirects above are
the only redirects followed anywhere in this flow. An operator-typed
URL stands outside all of this by design: an invite line's URL, or one
set later with `peer set` (`SetPeerURL`), was typed, not discovered, so
a deliberate `http` or LAN address stays possible exactly there, at the
operator's word, and nowhere by discovery.

**Mail intake — unchanged; the yard address's use for mail is
dropped, and mail is never at the edge.**
The fact that settles it is §2.3: mail already enters through a hook
the yard verifies itself, in the operator's own Cloudflare account, and
mckean has already refused the edge doing users' mail (yardr-adza).
The intake itself is kept exactly as it works today; what is dropped
is any use of the yard's HTTPS address as a mail address or as the
place a mail alias is discovered.
What is true today is narrower than an alias at the operator's domain:
the `cloudflare-email` pack (yardr-packs) hardcodes one address,
`yardmaster@yardr.dev`, in its declaration and its setup, takes no
address parameter, and the WebFinger record carries no mail alias, so
the yard's address provides no generic or discoverable mail alias and
no operator-chosen one. The smaller choice is taken here: the "any
alias" claim is dropped, rather than parameterising the pack in this
design. The yard's HTTPS address is not a mailbox and `in.yardr.dev`
never receives mail. What the one-address idea buys mail is only a
shared name where the operator's domain and the hardcoded address
already coincide, and describing the hook's public URL
(`hook_public_url`, today `<edge>/hooks/<account>/<yard>`) as a door
at the address rather than a second name. No build is proposed for
mail beyond naming what exists today in the pack's setup (§6); the
intake itself works today.

**Peering by address — kept; this is the use that pays.** The fact that
settles it is §2.4: today a link can only start with the two operators
already in contact — a code read aloud, a line carried by hand — so no
third yard can be named at all. With the record, linking gains a third
way beside pairing and invite lines. That way is yardr-4687's flow,
not a second one beside it: this design extends yardr-4687 §6 bead 4
and does not duplicate it. The verb is `yardr peer link <address>`,
the route is `POST /peers/link`, and the asked yard's operator
approval is part of the link, exactly as bead 4 specifies:

```text
$ yardr peer link https://north.example.com/laptop
looked up https://north.example.com/laptop: key a8tU…grY, endpoint https://north.example.com/laptop
asked laptop to approve this yard as https://my.yardr.dev/hal/desk: pending there
# on laptop: the pending ask, in a terminal there
$ yardr peer requests
$ yardr peer approve https://my.yardr.dev/hal/desk
```

`peer link` fetches the record through the guarded client above, and
stores the key it names only once the record's subject matches the
address asked for and its endpoint has passed the storage guard
above; a refused endpoint fails the link with its reason and stores
nothing, and every later call to that stored URL goes through the dial
guard above. That is deliberately not "as an invite line does": an
invite URL was typed by an operator and may be `http` or a LAN
address, and it keeps that freedom, while a record-supplied endpoint
was named by a host no operator chose and gets none of it. On the
asking side the peer is stored ping-only and awaiting approval, and
the asking yard sends it nothing — no push, no pull, no counted try —
until a checked `linked` answer or a signed message from it clears
the gate. On the asked side, `POST /peers/link` is verified in bead
4's order (recipient, possession under the claimed key, then a
guarded reverse lookup matching both keys and the endpoint), and a
verified ask is kept as a durable pending link (seven days, at most
32, one per signing key) until its operator answers with `yardr peer
requests`, `yardr peer approve` and `yardr peer decline` in a
terminal — never a peer message, never a run ask. Approval creates
only the ping-only link; decline keeps only the day's tombstone.
The lookup is enough to ask (Q3, decided, §5): no pairing-style
six-digit comparison is required beside it, because a stranger who
found the address in writing often has no one to compare digits
with. Yardr-4687's `yardr peer verify` remains what bead 4 says it
is — an optional, accidental-mismatch check only (about 20 bits,
not pairing's nonce-bound check) for operators who can compare it.
`peer allow` still grants nothing by default. Pairing and invite
lines stay: they are the bootstrap when no host serves the record,
and pairing's six-digit check stays the stronger root of trust
where the two operators can compare it — an address lookup trusts
the host's TLS once, inside that guard, while a pairing trusts only
the operators. Peering does not become open: a lookup makes a link
*proposable*, and what a link may carry (`peer allow`, the roster,
trust, intakes) is granted exactly as today.

The lookup is a one-time bootstrap, and nothing more (Q5, decided,
§5). The stored link keeps the endpoint and key the lookup returned and
remembers that its URL came from a lookup, so the dial guard above
applies; it does not keep the address as a thing to consult again, and
no call ever re-fetches the record to follow a moved yard. A yard that
moves, is renamed at the edge, or disconnects is linked again by hand
at its new address, exactly as a re-addressed invite line is carried
again today. Nothing about the wire changes here; RFC 9421 signing and
HPKE bodies are yardr-4687's beads, sequenced after this one (§6).

**BDP scope — dropped at the edge; kept only where the yard itself serves
BDP at its address.** The facts that settle it are §2.5: the scope is
already a URL, but BDP is not served at an edge address and is not safe
to serve there. A connected yard minting ids under
`https://my.yardr.dev/<account>/<yard>/` would name beads no client can
fetch at that URL — the edge link carries no BDP and the yard's listener
serves none — and making the edge serve them would put an
unauthenticated, whole-yard read (every title, body and metadata) on the
public internet. That route is not proposed (§4). What is kept is
narrower: a yard that itself serves BDP at its address — a self-hosted
yard whose listener or proxy answers at the address's host, run with
`--unsafe`/`--host` as today — may take `<address>/` as its scope.
`--scope` keeps winning where given, and a connected yard with no BDP
endpoint of its own keeps today's `http://<addr>/yard/` default: its
edge address names it for peering and mail, not for BDP. In every case
startup prints the scope it picked and where it came from (the flag, the
address, or the loopback default).

Changing a scope re-mints every bead and link id under it, and the cost
belongs in front of the choice, not after it. An id already shared — in
mail, in another yard's records, in a person's notes — names the old URL;
under the new scope the same bead is a different URL, and BDP's Read
profile has no redirect or alias that joins them, so the old ids stop
naming the records a reader now gets. Setting `yard_address` is therefore
the operator's explicit act of moving: before it, `yardr bdp serve
--scope http://<old-addr>/yard/` (the scope printed at every earlier
startup) keeps the old ids exactly, and bead 5 (§6) must print both
scopes and that choice when an address first gives a different one. A
team scope shared by several yards (`https://beads.example/team/`) is
untouched by all of this: scope is BDP's namespace, not the yard's name,
and the flag exists for deployments that choose one namespace on purpose
(Q4, decided, §5: the scope moves on the operator's explicit act only,
never automatically).

**Provenance a reader can verify — dropped under BDP v0; the path back is
a BDP extension, not a yardr field.** The fact that settles it is §2.5:
`writer-supplied` authenticates nothing and the yard has the key that
could — but BDP v0's closed schema leaves nowhere lawful to put the
evidence. `actorKey` or a signature beside the record, inside
`revisionProvenance`, or inside `revisionActor` all fail validation, and
the open `properties`/`metadata` bags would hide the fact where no BDP
client looks for it. So this proposal serves nothing new: provenance
stays `writer-supplied`, stated as honestly as today, until BDP itself
carries attestation. What that takes follows BDP Core's "Carried
attribution": `revisionProvenance` carries data, not evidence; it
transports attribution and attests nothing, and an authority-attested
form, if one arrives, is a *distinct member* with a distinct name —
`revisionProvenance` never becomes it, and `revisionActor`'s `basis`
stays `writer-supplied` or `unknown`. The ask in gastownhall/bdp is
therefore for that distinct attestation member, not for new fields
inside `revisionProvenance` or `revisionActor`, with the profile's
version and conformance story decided there, since a v0 reader must go
on reading what a yard serves. The attestation it would carry binds
three things under a defined canonical encoding — the Resource URI,
the revision, and the actor — so a valid attribution cannot be copied
onto another record, together with the writing yard's key and its
signature over that encoding. Only then does a yardr bead sign: one
signature per revision, made where the revision is already minted,
checked by any reader who fetches the yard's record, takes the key
and verifies that this yard recorded that actor for this revision of
this Resource URI. Even then it does not authenticate the *person*:
the yard vouches for its store's record of an actor, no more — the
same line yardr draws everywhere ("Who is acting",
[users.md](../users.md)). Asking BDP for that extension now is decided
(Q2, §5): bead 6 (§6) files it in gastownhall/bdp, and bead 7 (§6) is
written so nothing in yardr moves before it lands.

### 3.3. (3) A self-hosted yard with no edge

Nothing above needs the edge. A yard with no edge account:

- has its key, as today, and an address only when the operator names its
  domain (`yardr settings set yard_address https://north.example.com/laptop`;
  Q1, decided, §5) and serves the record there — `yardr bdp serve` already
  answers on the scope's host, and the same listener serves the WebFinger
  record beside it. The `yard_address` setting itself — its place in
  `KnownSettings` ([internal/store/settings.go](../../internal/store/settings.go))
  and its check, an absolute `https` URL on port 443 only (an explicit
  port other than 443 is refused) with no user, query or fragment, in
  the shape of `CheckHookPublicURL` there — is bead 1's work (§6); a
  value naming another port cannot pass the setting check and then fail
  every guarded lookup (§3.2);
  until that bead lands the setting does not exist and no yard has a
  self-hosted address;
- is linked by pairing or invite line with no record at all, exactly as
  today, and by address where it serves one;
- serves BDP under its address where it serves BDP there, or under any
  `--scope`, as today (§3.2 gives the migration its setting implies);
- gets verifiable provenance only when BDP carries it (§3.2), verified
  then against the record at its address or the key on a link it already
  holds; under BDP v0 it has `writer-supplied`, as every yard does;
- takes mail only through its own domain's hook, as today, or not at all.

The edge is a directory and a carrier for yards that have one; it is not a
root. No use in §3.2 is refused to a yard without one, and none phones home.

### 3.4. What is new, in one list

One settings value (`yard_address`, bead 1) for yards with no edge;
`yardr edge set findable on|off` over the existing edge link (beads 2
and 4), so findability can be turned on or revoked after enrolment; the
WebFinger record endpoint yardr-4687 already specifies, served by the
edge only for findable yards (bead 2, extending yardr-4687 bead 2) and
by `yardr bdp serve` for self-hosted ones (bead 3, reusing
yardr-4687 bead 4's guarded client and record format rather than a
second implementation); `peer link` by address (bead 4, which is
yardr-4687 §6 bead 4's verb, route and pending-approval flow extended
to `yard_address`, not a second link flow), fetching its record through
that guarded client and storing and dialling the endpoint it returns
only under that design's storage and dial guards (§3.2), once, with no
later refresh from the address (Q5, decided, §5); and the BDP scope
taken from the address only where the yard serves BDP at that address
(bead 5). Provenance adds nothing until BDP does (§3.2; beads 6 and 7).
Everything else — the key, the edge name, pairing, invite lines, hook
mail intake, `--scope`, the roster and grants — exists.

### 3.5. What the address opens, and what closes it

Five ways an address could be turned against a yard, each with what
closes it or why it is accepted:

1. **A record steering calls into private services.** A hostile or
   compromised host returns an endpoint on a LAN or cloud-metadata
   address, and the linking yard dials inward on its next peer call.
   Closed: the lookup and every dial after it go through yardr-4687
   §3.4's guarded client — subject match, public-host and
   global-address checks before any dial, refusal at storage, literal
   dialling with no redirects (§3.2; beads 3 and 4 carry the checks as
   acceptance, reusing yardr-4687 bead 4's client where it has landed).
2. **A public read of the whole yard.** `yardr bdp serve` is
   unauthenticated (§2.5), so an address that also served BDP would
   publish every bead's title, body and metadata. Closed at the edge:
   BDP scope is dropped there and no route carries BDP through it
   (§3.2). Accepted, and stated, for the self-hosted case: a yard that
   serves BDP at its own address publishes what it serves, by its
   operator's act of serving it there.
3. **Another key handed to a newcomer at first link.** The lookup is
   enough to ask (Q3, decided, §5), so whoever controls the domain, the
   edge account or the DNS on first-link day can make a first link pin
   a key of their choosing. Accepted, and limited: the key is printed
   at the link, pinning holds it against every later answer, the asked
   yard's operator must still approve the pending ask before anything
   is granted, `peer allow` grants nothing by default, and pairing's
   six-digit check remains for operators who can compare one (§3.2).
4. **A reused name after a rename or a disconnection.** Pinning the
   key protects authentication of the *answer*; it does not stop
   delivery of the *request*. A link made by address stores the
   endpoint URL once (Q5, one-time bootstrap), and the edge path
   `/peers/<account>/<yard>` is that URL. `Caller.do`
   ([internal/yards/caller.go](../../internal/yards/caller.go)) sends
   the signed request body to the stored URL, and the receiver
   ([internal/yards/receiver.go](../../internal/yards/receiver.go))
   reads the body before verifying its signature, so a replacement
   yard answering at a reused path would receive readable bodies
   from existing links even though it cannot answer as the old yard.
   Today's bodies are readable; yardr-4687's HPKE-sealed bodies are
   the later fix that would remove the need for this rule, and are
   not a prerequisite claimed here. Closed by rule, not by pinning:
   the edge never reissues a retired `<account>/<yard>` name and
   tombstones it (§3.1), so no later yard answers at a path an
   existing link still stores. The cost is that an operator cannot
   reclaim their own old name. A fresh lookup at a retired name is
   therefore 404, and a record naming another key at a live name is
   still refused and shown (§3.2).
5. **Authorship taken on trust.** Until BDP signs provenance,
   `writer-supplied` names an actor nothing authenticates (§2.5), and
   a bead read on another yard can claim any writer. Accepted for now,
   stated in §3.2 rather than papered over with fields the closed v0
   schema refuses; closing it is the gastownhall/bdp ask (Q2, decided,
   §5; bead 6).

## 4. Left out

What was considered and is not proposed, named as the bead asks:

- **Mail hosted at the edge (`<yard>.<random>@in.yardr.dev`).** Refused
  already by mckean and by yardr-adza: the edge would read users' mail, hold
  its secrets and become a mail operator. Mail stays as the pack works
  today (§3.2), with no alias provided by the yard's address.
- **A parameterised mail alias in the `cloudflare-email` pack.** Making
  the pack take an operator-chosen address would give the yard's address
  a discoverable mail alias, at the cost of pack parameters, setup and
  tests for a naming coincidence. The smaller choice is taken (§3.2):
  the "any alias" claim is dropped and the pack's hardcoded address
  stands as it is.
- **A mail address as the yard's primary address** (`acct:<yard>@<domain>`
  as the identifier itself). One use of four; it cannot be the thing looked
  up for keys, and WebFinger already accepts the HTTPS URL as its resource.
  Nothing forbids serving it later as an alias of the same record.
- **`did:web` and DID documents.** The same keys with a resolver and
  vocabulary yardr does not otherwise use; WebFinger carries the three
  facts needed. (yardr-4687 §4 reaches the same end; not reopened.)
- **A second key for the address, or a registry of yards.** The peer key
  already is the identity (§2.1); a registry is a new root that self-hosted
  yards could not join, and a directory is what the edge already is for
  its own yards.
- **Renaming peers, crew addresses or user identities to the new scheme.**
  `crew@<peer>` names a person at a yard this one is linked with, keyed by
  the link; `user identity` names a person in a source. Both are about
  people, keep their forms, and gain nothing from the yard's URL.
- **Matrix, ActivityPub or any new transport for peering.** Weighed and
  settled in yardr-4687; the address rides the link that exists.
- **Authenticating the person in provenance.** The yard's signature vouches
  for its store's record of an actor, never for a human. Stronger claims
  need device and user keys, which are a design of their own.
- **BDP served at or through the edge address.** No route carries it
  today, and the honest version of one is a public, unauthenticated read
  of the whole yard (§3.2). A yard that wants its beads read at its
  address serves BDP there itself; an authenticated BDP profile is a
  design of its own, in BDP first.
- **Provenance fields under BDP v0, in any spelling.** Beside the record,
  inside its provenance, or smuggled into `properties`/`metadata`: the
  first two fail the closed schema, the third hides the fact from every
  BDP client (§2.5, §3.2). Nothing is served until the spec carries it.
- **Following a moved yard, or an edge address that outlives its yard.**
  No link re-reads its address, no endpoint is refreshed, and the edge
  keeps no renamed or disconnected yard's old address pointing on (Q5,
  decided, §5). One-time bootstrap is the whole mechanism until yards
  move in practice often enough to ask for more.
- **Building any of it.** This document only; the beads are §6, unfiled.

## 5. Open questions

Q1 to Q6 were to decide at `decide` and are decided, recorded below as
they were settled on 5 Nov (asks 20669 to 20673 and 20714). The
proposal above already reads
as they decide. None of Q1 to Q6 is decided anywhere else in this
document except as recorded here.

**Q1. How does a yard with no edge get its address: a setting the
operator writes (`yard_address`), or derived from `--scope`/the BDP
host it already serves?** Setting: one more name to keep true, but
explicit and available before BDP runs. Derived: nothing new to set,
but the address then changes when the serving name does, and a yard
that serves no BDP has none. *I would take the setting* — the address
must be stable before anything is served under it, and stability is
its whole point. **Decided (5 Nov, ask 20669): the setting.** The
operator writes `yard_address` once (§3.3, bead 1); nothing is derived
from the BDP host.

**Q2. Does yardr ask BDP for a signed-provenance extension now, or
drop verifiable provenance for good?** Serving fields first is not an
option: BDP v0's schema is closed (§2.5), so there is no "now" that
validates. Ask now: an issue in gastownhall/bdp for a distinct
attestation member beside `revisionProvenance` (which stays as it
is), carrying the writing yard's key and a signature binding the
Resource URI, the revision and the actor under a defined canonical
encoding, with the profile's version and conformance story decided
there, then one yardr bead (§6, bead 7) — at the cost of waiting on
another project's decision. Drop for good: `writer-supplied` stays
the whole story, and a bead read on another yard never names its
writer verifiably. *I would take ask now, build nothing meanwhile* —
the want is real (mckean's "even the bead definition supports
this"), and an unanswered ask costs one filing, where a dropped use
costs the provenance the bead asked for. **Decided (5 Nov, ask
20670): ask now.** Bead 6 (§6) files the issue in gastownhall/bdp;
yardr builds nothing unless BDP adopts it, and bead 7 (§6) is closed
as dropped if it is refused.

**Q3. Is an address lookup enough to ask for a link, or must the
first link by address also pass a pairing-style check?** Lookup
enough to ask: `yardr peer link <address>` fetches the record,
prints the key, stores the asking side ping-only and sends the
`POST /peers/link` ask, trusting the host's TLS at first sight inside
§3.2's guard; the asked yard's operator still approves the pending
ask before anything is granted, and `peer allow` grants nothing by
default. Check required beside it: a second round trip and six digits
to compare before even asking, which strangers who found the address
in writing often cannot do. *I would take lookup enough to ask* —
it matches invite lines in how the endpoint it dials was checked
(§3.2 keeps the two apart), and yardr-4687's `peer verify` stays
available as an optional accidental-mismatch check, not a gate.
**Decided (5 Nov, ask 20671): the lookup is enough to ask.** No
six-digit check is required beside it (§3.2, bead 4); the pending
approval in that flow is unchanged.

**Q4. When a yard's address first gives its BDP scope, who chooses the
new ids?** Explicit only: the scope changes when the operator sets
`yard_address` or passes `--scope`, startup prints the old and new
scopes and the `--scope` line that keeps the old ids, and nothing moves
for a yard that does neither. Automatic: a yard serving BDP at its
address gets `<address>/` as its default with the same printed warning
— one less step, and ids change under a yard whose operator may not be
watching that startup line, breaking every id already shared. *I would
take explicit only* — durable ids are the gain, and a silent re-mint
takes it away in the act of giving it. **Decided (5 Nov, ask 20672):
explicit only.** The scope moves on the operator's explicit act alone
(§3.2, bead 5).

**Q5. Must an edge-issued address outlive a rename or a disconnection,
and do address-based links follow a moved yard?** Raised at critique:
the edge controls that locator, and the yard has it only while
connected (§3.1), while §3.2 saves the endpoint a lookup returns
without saying whether it is ever looked up again. Options: (i)
one-time bootstrap — the lookup only makes the link; a moved yard is
linked again by hand; (ii) links refresh — a link keeps the address
and re-reads it when calls fail, so a moved yard is followed, and the
edge keeps a renamed yard's old address pointing on. Cost: (i) asks
the operator to re-link the rare move by hand; (ii) keeps a second
live name per link, a refresh path that must itself pass §3.2's guard
every time, and an old-address forwarding promise at the edge that a
disconnected yard cannot keep. *I would take (i)* — nothing needs
refresh yet, and it can be added once yards move in practice.
**Decided (5 Nov, ask 20673): (i), one-time bootstrap.** The lookup
only makes the link; no endpoint is refreshed and the edge owes a
renamed or disconnected yard's old address no forwarding (§3.1, §3.2,
§4).

**Q6. May the edge ever give a retired yard name to someone else?**
Raised at critique round 4: a link made by address stores the edge
endpoint once (Q5), so if the edge reissued a retired
`<account>/<yard>` name, existing links would still send their
signed, readable request bodies to that path, and a replacement yard
answering there would receive them (§3.5) even though key pinning
stops it answering as the old yard. Options: (i) never reuse — a
retired name is tombstoned forever, at the cost that an operator
cannot reclaim their own old name; (ii) reuse after a wait (for
example 90 days), which still exposes any link left unused that
long. Yardr-4687's HPKE-sealed bodies would remove the exposure and
with it the need for this rule, but that work is not a prerequisite
here. *I would take (i)* — it closes the leak at no build cost, and
reclaiming an old name is rare enough to add later if practice asks
for it. **Decided (5 Nov, ask 20714): (i), never reuse.** A retired
edge yard name is tombstoned forever and nobody can take it, its
owner included (§3.1, §3.5).

## 6. The beads it would become

In landing order, one bead per depot: a bead touches one depot only, so
no builder lands half a change in a repository they are not working.
None is filed in this pass; building waits until after launch, and the
beads are filed for mckean's choice of the decided design.

The record format and the guarded client shared with yardr-4687 have
one owner: **yardr**. The format is yardr-4687 §3.1's WebFinger record
and the client is that design's §3.4 guarded fetch, with its storage
and dial guards, both as specified there. Bead 3 in this depot reuses
that client and format where yardr-4687 has already landed them, and
implements them only where it has not — one implementation in yardr,
never a second one beside it. The yardr-hosted bead below serves
exactly that format from the enrolment record the edge already holds;
it defines no format of its own.

This design extends yardr-4687 §6 bead 4 and does not duplicate it.
Bead 4 there already owns address linking (`yardr peer link`,
`POST /peers/link`, the pending-approval flow in a terminal, the
findable setting and `peer verify`). Beads 2 to 4 below are therefore
reduced to what that design lacks for this address — the
`yard_address` locator beside the edge locator, findability after
enrolment, the retired-name
tombstone, and serving the record self-hosted — and each names its
dependency on the yardr-4687 bead it extends. A builder works one
bead in one depot; where a yardr-4687 bead has already landed the
same work, the bead below is only its delta, and where the delta is
empty it is closed as covered rather than built again.

1. **Name the yard's address, and add the `yard_address` setting
   (yardr)** — add `yard_address` to `KnownSettings` with its check (an
   absolute `https` URL on port 443 only — an explicit port other than
   443 is refused — with no user, query or fragment; §3.3), then
   `yardr edge status` and `yardr doctor` print the yard's address:
   `yard_address` where it stands and, for a connected yard, its edge
   locator too — both where both stand (§3.1), otherwise the one that
   stands, otherwise the key alone. Look in
   [internal/store/settings.go](../../internal/store/settings.go)
   (`KnownSettings`, `CheckHookPublicURL`) and
   [internal/cli/edge.go](../../internal/cli/edge.go). Done when: the
   setting accepts a yard's own-domain URL on port 443 and refuses an
   explicit non-443 port, a user, a query and a fragment, and both
   commands print the address on connected and unconnected yards
   (both locators where both stand, either where only one does),
   covered by the
   store's and commands' tests. No dependency; every later bead reads
   the address this one defines.
2. **Serve the yard's record at the edge, findable yards only
   (yardr-hosted)** — extends yardr-4687 §6 bead 2 there, which is the
   same edge work, not a second serving: the edge answers the
   WebFinger lookup at a connected yard's address (§3.1) with that
   yard's record in the format yardr owns (intro above) — peer
   endpoint and signing key from the enrolment record it already
   holds, and the encryption key once yardr-4687 presents it — only
   for a yard whose operator turned findable on. Every other yard is
   404: unknown, unfindable, and a retired name, which the edge
   tombstones and never reissues (§3.1, §3.5). Setting `yard_address`
   does not suppress this record: a yard with both locators is served
   here and at its own address, both naming the same key (§3.1). The
   edge also takes findability after enrolment: an update carried over
   the yard's existing edge link (`yardr edge set findable on|off`,
   the yardr half in bead 4) flips the stored bit, and the next lookup
   reflects it. Look in yardr-4687
   §3.1, its Q4 and §6 bead 2 there. Done when: a lookup of a
   findable connected yard's address returns its endpoint and signing
   key exactly as enrolled; a lookup for an unknown yard, for an
   enrolled yard whose findable is false or absent, and for a retired
   name are each 404 (tested,
   not merely unlisted); a retired name later enrolled by another
   yard is still 404 and never serves the new yard; a yard that sets
   `yard_address` is still served here while findable, with the same
   key as at its own address; turning findable off over the edge link
   makes the next lookup 404 and turning it on makes the record
   answer again, each tested at the edge; two yards at the
   edge are never served each other's record; and the router's tests
   pass. Depends on yardr-4687 bead 2 there for the record serving it
   extends; where that bead has landed, this bead is the findable
   gate after enrolment and the tombstone alone. It
   serves what enrolment already holds and dials no yard to answer.
3. **Serve the record self-hosted, reusing the guarded client
   (yardr)** — what yardr-4687 lacks for this address: `yardr bdp
   serve` serves the same record for a self-hosted yard at its
   `yard_address`, and the yard's fetch of any address goes through
   yardr-4687 §3.4's guarded client (§3.2), reused from yardr-4687
   §6 bead 4 where that bead has landed it and implemented here only
   where it has not — one client in yardr, as the intro above states.
   This bead owns that record format and client interface in yardr.
   Look in yardr-4687 §3.1 and §3.4 and
   [internal/bdp/server.go](../../internal/bdp/server.go). Done when: a
   lookup of a self-hosted address returns endpoint and signing key; a
   record whose `subject` differs from the requested address is
   refused with nothing kept, as is a record naming another key than
   the one already pinned; a claimed address whose host is an IP
   literal, `localhost`, a single label or a local suffix, or whose
   host resolves to a private, loopback, carrier-grade-NAT
   (100.64.0.0/10), documentation or IPv4-translation (64:ff9b::/96,
   64:ff9b:1::/48) address, is refused before any dial; an `http`
   redirect, or one to a host failing those checks, is not followed;
   an answer over 32 KiB is not kept, and a lookup past a §3.4 quota
   fetches nothing; and two yards at one host are never served each
   other's record, the lookup cache being keyed by the full normalised
   resource URL. Depends on 1 for the address it serves, and on
   yardr-4687 bead 4 for the guarded client it reuses.
4. **Link by address: `peer link <address>` (yardr)** — extends
   yardr-4687 §6 bead 4, which owns this flow: `yardr peer link`
   fetches the record through bead 3's guarded client, prints the key,
   stores the asking side ping-only and awaiting approval, and asks
   over `POST /peers/link`; the asked yard verifies that route in
   bead 4's order and keeps verified asks as pending links for its
   operator's `peer requests` / `peer approve` / `peer decline` in a
   terminal. This bead adds only what bead 4 lacks for this address:
   `yard_address` (bead 1) as a link address beside the edge address,
   under the same storage and dial guards, with the stored link
   remembering that its URL came from a lookup. It also adds
   findability after enrolment: `yardr edge set findable on|off`
   sends the new value over the yard's existing edge link for the
   edge to store (bead 2), extending yardr-4687 bead 4, where
   findable is presented at enrolment only. No six-digit check is
   required beside the lookup to ask (Q3, decided, §5), the pending
   approval is unchanged, nothing is re-fetched when a yard moves
   (Q5, decided, §5), and pairing and invites are unchanged, their
   operator-typed URLs outside the guard. Look in yardr-4687 §3.4 and
   §6 bead 4 there, [internal/cli/edge.go](../../internal/cli/edge.go)
   (the `edge set findable` command and the edge link it rides),
   [internal/cli/peers.go](../../internal/cli/peers.go),
   [internal/store/peers.go](../../internal/store/peers.go) and
   [internal/yards/caller.go](../../internal/yards/caller.go) (the
   guarded dial for lookup-stored URLs). Done when: two yards link by
   address — at an edge address and at a `yard_address` — through the
   pending flow (the asking side sends the gated peer nothing until a
   checked `linked` or a signed message clears the gate, approval
   creates only the ping-only link, decline only the day's tombstone)
   and `peer ping` then passes both ways; a changed-key record is
   refused with the old key shown; a record naming an `http` endpoint,
   an IP-literal host, or a host resolving to any non-global address
   is refused at storage with the reason shown and no peer and no
   pending record stored; an endpoint that rebinds to a non-global
   address between link and dial is never dialled (the guarded dial
   counts a failed try), and a peer endpoint answering a redirect to
   another host, private or public, is not followed to it; a yard
   added by invite with an operator-typed URL, a LAN `http` one
   included, is still called at that URL as today; and `yardr edge
   set findable off` over the edge link makes the yard's next edge
   lookup 404 while `on` restores it, each shown by the command and
   covered with bead 2's edge-side tests. Depends on 3, on 2
   for a yard found at its edge address, and on yardr-4687 bead 4 for
   the link flow it extends; where that bead has landed, this bead is
   the `yard_address` form and the post-enrolment findable toggle.
5. **Take the BDP scope from the address only where BDP is served
   there (yardr)** — a yard serving BDP at its `yard_address` host with
   no `--scope` mints ids under `<address>/`; `--scope` still wins; an
   edge address alone changes nothing; the scope moves only on that
   explicit act (Q4, decided, §5); startup prints the scope, where it
   came from, and — when an address first gives a different scope —
   the old scope and the `--scope` line that keeps the old ids (§3.2).
   Look in [internal/cli/bdp.go](../../internal/cli/bdp.go) and
   [docs/bdp.md](../bdp.md). Done when: a client on another host
   fetches `bdp.json` and one bead under the address scope and gets
   both (a real remote read, not only minted ids), an explicit scope
   is untouched, a connected yard with no BDP endpoint keeps its
   loopback default, and the conformance suite is green. Depends on 1;
   independent of 2, 3 and 4.
6. **Ask BDP for signed provenance (gastownhall/bdp)** — file one issue
   in gastownhall/bdp for a distinct attestation member beside
   `revisionProvenance`, per BDP Core's "Carried attribution"
   (§3.2): `revisionProvenance` and `revisionActor` stay as they are,
   and the new member would carry the writing yard's key and its
   signature binding the Resource URI, the revision and the actor
   under a defined canonical encoding, so an attribution cannot be
   copied onto another record, with the profile's version and
   conformance story decided there (Q2, decided, §5). Done when: the
   issue is filed naming the closed v0 schema (§2.5), the distinct
   member asked for and what its signature binds, and that a v0 reader
   must go on reading what a yard serves. No yardr code depends on its
   filing; bead 7 depends on its adoption.
7. **Sign provenance in the served records (yardr)** — only once bead
   6 is adopted in gastownhall/bdp: write the adopted attestation
   member beside `revisionProvenance`, signed with the yard key over
   the Resource URI, the revision and the actor under the adopted
   canonical encoding, where records are built, so what is served
   carries it. Look in [internal/bdp/graph.go](../../internal/bdp/graph.go)
   (`provenance()`, `BuildGraph`, `CachedLoader`). Done when: a test
   drives the served graph through `CachedLoader` and the server —
   not the helper alone — fetches a bead, a dependency link and an
   issue link, verifies each attestation against the key from the
   yard's record, fails on a tampered actor id and on an attestation
   copied onto another Resource URI, and the conformance suite passes
   against the extended profile. Depends on 6 (adopted) and on 3 for
   key discovery; independent of 4 and 5. If 6 is refused, this bead
   is closed as dropped, not worked around.
8. **Say it in yardr's docs (yardr)** — [peers.md](../peers.md) gains
   linking by address (`peer link`, the pending approval, and
   findable), [bdp.md](../bdp.md) the address scope and its migration
   (and, when bead 7 lands, the attestation member),
   [edge.md](../edge.md) the address line, `yardr edge set findable
   on|off`, the findable gate and the
   retired-name tombstone. Done when: each page's commands run as
   printed against a local yard. Depends on 4 and 5, and on 7 for its
   provenance part, so it documents what exists.
9. **Name mail as it works today in the pack's setup (yardr-packs)** —
   the `cloudflare-email` pack's setup names what is true today
   (§3.2): the one hardcoded address it already routes to the Worker
   and hook it sets up, with no operator-chosen alias provided by the
   yard's address. The smaller choice is taken — the "any alias"
   claim is dropped; this bead parameterises nothing. Done when: the
   setup as printed takes mail at that address into the yard, against
   a local yard, and names no other alias. No dependency; it documents
   an intake that works today.

What yardr-4687 already owns is not built again here: RFC 9421
signing, HPKE bodies, the encryption key in this same record, and the
core of its §6 beads 2 to 4 (edge record serving, `/peers/link`
forwarding at the edge, and the yardr link flow itself). Beads 2 to 4
above extend those beads and land with or after them, on the one
record implementation the intro above assigns to yardr; a bead whose
delta yardr-4687 has already landed is closed as covered.
