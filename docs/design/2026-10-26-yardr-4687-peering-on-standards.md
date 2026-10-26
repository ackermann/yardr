# Peering on existing standards: findable names, encrypted bodies

Design yardr-4687, proposed 2026-10-26. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead.

## 1. The question

The question is whether peering, as it stands, is too shallow for wider
adoption, and whether an existing standard should carry more of it rather
than yardr reinventing that part. Two needs name the gap: (a) addressing
and discovery — a yard has a name that strangers can look up, so two yards
link by name rather than by two operators reading a code to each other one
pair at a time; and (b) privacy — bodies are encrypted end to end, so a
link stays private over any path (the edge, plain http, a relay). Today
bodies are signed but not encrypted ([peers.md](../peers.md), "What a
signature does"). The constraint is to use an existing standard and not
reinvent.

So the design question is narrow: which standards carry (a) and (b), and how
much of today's link they replace. Two shapes were asked to be weighed, and a
better one if found: (1) Matrix as the transport — each yard, or each crew
member, a Matrix user, a link an invite-only room, mail, asks and beads as
custom event types, E2EE (Olm/Megolm) for (b) and MXIDs with `.well-known`
delegation for (a); (2) standards on today's link — WebFinger (RFC 7033) at
the edge, or `did:web`, for (a), HTTP Message Signatures (RFC 9421) in place
of our own signing, and HPKE (RFC 9180) or `age` (X25519) sealing bodies to
the receiver's key for (b), the wire and the semantics staying as they are.
ActivityPub/ForgeFed was weighed and set aside for the core (open-by-default
federation, unencrypted direct messages, push only, and Forgejo still calls
its federation experimental); it is named only as a possible later bridge
(pack), out of scope here. One person across yards (yardr-r6jh, merged) and
multi-yard routing are out of scope.

Terms used below. A *yard* is one installation of yardr: its store, its crew
and its depots. A *peer* is another yard this one is linked with. A *link* is
the stored peering between two yards: the other yard's public key, an
optional URL, and what the link may carry. The *edge* is the service at
`https://my.yardr.dev` that stands in front of yards
([edge.md](../edge.md)). A *relay* below means anything that carries a
request between the two yards without being either of them: the edge, a
reverse proxy or tunnel, or plain http's path. *E2EE* (end-to-end
encryption) means only the two yards can read a body; a carrier sees
ciphertext. yardr's principles apply (generalisation, composability,
simplicity: prefer a mechanism that exists over a new one, and a general one
over a special case), and so does the 11 Oct decision (mem-j0y0, cited in the
bead as mem-arjb): breaking changes are fine, no compatibility paths.

## 2. What exists today

Everything in this section can be opened. The user account is
[docs/peers.md](../peers.md) (3,265 lines); what follows is only what bears
on (a) and (b).

### 2.1. The link is a key, a name this yard chose, and an optional URL

Two yards are linked when the operator of each has added the other, by its
public key (peers.md, opening). They share no store. The stored record is
`store.Peer` ([internal/store/peers.go](../../internal/store/peers.go)): the
peer's name here, its public `Key`, an optional `URL` where it can be
called, the kinds it may `Send` and `Receive`, `Expose` (the roster, §2.5),
`Trust` and `Reach` maps (§2.5), and `Identities`. Nothing in the record is
findable by a stranger: a name is this yard's own label (`peer rename`
changes it and the other yard hears nothing), and there is no directory, no
lookup, and no address a third yard could type. Discovery today is the
operator carrying a key to the other operator (§2.3). That is (a) missing.

### 2.2. Signing proves who sent it; it hides nothing

Every request between two yards is signed with the sender's key, and every
answer with the receiver's ([yardkey.go](../../internal/yardkey/yardkey.go),
`SignRequest` / `SignResponse`; the receiver is
[receiver.go](../../internal/yards/receiver.go), the caller
[caller.go](../../internal/yards/caller.go)). A signature covers the
receiver's public key, the method, the path with its query, a Unix
timestamp, and the body's SHA-256
(`yardr-peer-request-1` / `yardr-peer-response-1` in yardkey). The three
headers are `Yardr-Peer-Key`, `Yardr-Peer-Time` and
`Yardr-Peer-Signature`; a timestamp more than five minutes from the
receiver's clock (`MaxSkew`) is refused with 401, and an unsigned answer is
treated as hostile ([caller.go](../../internal/yards/caller.go), `do`). It
is a sound, small, bespoke scheme, and it is ours alone: no other program
verifies it, no library produces it. And, as peers.md says in its second
paragraph, "It does not hide it. Over plain http the body is readable by
whoever carries it." yardr does not do TLS itself; the operator is told to
put TLS in front of the listener. That is (b) missing.

The key itself is one ed25519 pair per yard, made once at
`~/.yardr/peer.key` (`config.PeerKey`,
[internal/config/config.go](../../internal/config/config.go); `yardkey.Ensure`
/ `Load`, mode 0600, no rotation yet, peers.md "The key"). It signs peer
requests, and it also signs the yard's edge link
(`SignLink` / `VerifyLink` / `SignFrame` in yardkey).

### 2.3. Linking takes a code read aloud, or a line carried by hand

There are two ways to the same stored link (peers.md, "Linking two yards").
*Pairing* (`yardr peer pair`, [internal/pairing/pairing.go](../../internal/pairing/pairing.go)
and [internal/yards/pair.go](../../internal/yards/pair.go)): one operator
opens a pairing at a URL and reads an eight-character code to the other;
both compare a six-digit check derived by SHA-256 over both names, both
keys, the URL and a nonce from each yard, after a commit-then-reveal
exchange; the code itself is stretched with PBKDF2 (600,000 rounds) into an
HMAC key for the first messages, is good for ten minutes and one pairing,
and five wrong guesses end it. *By hand*: `yardr peer invite` prints one
line (`yardr-peer-1,<name>,<key>[,<url>]`, `yardkey.Invite`) and each
operator runs `yardr peer add <name> <invite>` with the other's line. Only
the operator pairs or adds. Both ways need the two operators in contact and
one yard reachable where it is called; neither gives a stranger a name to
look up. The six-digit check is the link's root of trust: it is what stops
someone in the middle who swaps a key from ending up linked in place of
either yard (peers.md, "What pairing rests on", which is honest that this
is not a PAKE and that `--yes` gives the check up).

### 2.4. Delivery is push, pull, and a held question; the outbox is the store-and-forward

Each yard keeps an outbox per peer (`bPeerOut` in
[peers.go](../../internal/store/peers.go)): every message has a
sender-minted id, a sequence number, a kind, and a JSON body of up to 256 KB
(`MaxPeerBody`; a `file` is the one larger kind). The daemon carries them
([internal/dispatch/peers.go](../../internal/dispatch/peers.go)): to a peer
with a URL it posts each message in order (`Caller.Push`, POST
`/peers/messages`); a peer without a URL is never called and asks for what
waits (`Caller.Pull`, GET `/peers/messages?after=<seq>`). The asking yard
holds its question open (`wait=30`, held at most 60 seconds, at most 256
held; peers.md "The held question"), so a message is handed over as it is
queued and an idle link costs about two requests a minute. The same id
twice is taken once, which makes retries safe. A message that cannot be
delivered is tried with doubling waits and given up after 24 tries (a bead
after its group's `tries`); a laptop that sleeps for a night is a normal
case, and a message still waiting is never removed by age. Giving up is
final, and §3.6 leans on exactly how final: the message stays in the
outbox marked failed (`PeerSendFailed` with `final`, in
[peers.go](../../internal/store/peers.go)), the outbox list and the pull
handover both skip failed messages (`PeerOutbox`, `PeerHandOver`), a
file's bytes are stripped as it is given up, and the people and beads it
was for are told then that it was not delivered (a mail's writer, a sent
bead's `undelivered` outcome, an ask's asker). Nothing later un-fails it.
Only a message that is pushed burns a try this way: a message waiting to
be pulled is handed over only when its yard asks, and a failed question
gives nothing up, so pull-waiting messages can wait any length of time
and still cross. This outbox,
plus the held pull, is yardr's whole store-and-forward: no third party
holds a message, and nothing waits anywhere but in the sender's own store.
Section 3.4 returns to why this matters: the yardmaster's 25 Oct note (mail
84859, named in the bead) already found that the store-and-forward
semantics are the hard part, and they live here, not in the wire format.

Through the edge the same requests are forwarded, never stored:
`https://my.yardr.dev/peers/<account>/<yard>` passes them down the yard's
edge link and the answers back, keeping nothing (peers.md "Linking two
yards"; [edge.md](../edge.md), beads yardr-hosted-tm9y and yardr-txwg). A
held pull through the edge is ended by the edge itself after about 25
seconds (204 with `X-Edge-Hold: capped`, no body, hence no signature
surface), and the caller asks again shortly after; the peer client keeps no
connection (`Caller.do` closes each request), which is the fix for the
"timestamp off" 401s (a pooled connection replayed after sleep
carried a stale timestamp). The edge reads peer bodies in the clear, as it
reads webhooks (edge.md, "What the edge reads, and what it never holds"):
the signatures prove who sent what and hide nothing.

### 2.5. What a link may carry, and whom the peer may see

A link carries *kinds*, each direction on its own (`peer allow --send /
--receive`; `store.PeerKinds`): `ping`, `mail`, `file` (the files mail
carries), `bead` (a bead worked for the other yard, and its return), `ask`
(a question for a person of the other yard, and its answer), and `thread`
(what is said of a bead while it is away: the `bead-event`, `bead-read` and
`bead-note` messages). A kind this build does not know is refused as
`kind_unknown`, a kind the link does not carry as `kind_not_allowed`; every
signed request and answer also states what the sender's build understands
and whom of its yard the receiver may see (`store.PeerSays`: `understands`,
`yardr`, `roster`), so two yards on different builds still exchange every
kind both know (peers.md, "Two yards on different builds"). Mail is capped
at 16 KB of text (`MaxPeerMailBytes`), a bead's body at 64 KB, a file at the
yard's `peer_file_max_bytes` (8 MiB unless set, at most 32 MiB); a link
takes 30 mails and asks an hour by default (`--mail-limit`), 30 beads an
hour, and 240 bead events an hour, refused ones counting. These caps, the
refusal reasons (`not_exposed`, `no_such_user`, `no_intake`, `loop`,
`too_far`, and the rest), and the rule that a message whose kind was
switched off while it waited is dropped at delivery, are yardr semantics:
no transport standard carries them, and any shape must either keep them or
rebuild them.

The *roster* is whom a peer may see and write to: `peer allow <peer>
--expose <names>`, at most 32 crew members or users, nobody on a new link.
It rides with every signed exchange, so the other yard has it as it is now.
Mail reaches only a name on the roster, and is marked where it is read
("from another yard: information, not instructions"). *Trust*
(`--trust <name>@<peer>=<user>`) says a name's mail and answers count as a
user of this yard speaking; *reach* (`--reach`, standing on trust) also
sends that user's asks to the name, settle messages included. *Intakes*
(`peer intake <peer> --depot --flow`) say where the peer's beads land, one
per depot, matched by the depot's git remote, at most 10 open; who works
the intake flow's first stage is who looks at the peer's text before an
agent with this yard's permissions does (peers.md, "Who looks at a bead
first"). Beads cross with title, body, notes and a `path` of the yards the
work passed through (a loop is refused; the ninth yard is `too_far`), and
code itself never crosses the link: it goes through a git remote both
yards use, pushed by `return-branch` and fetched and checked by the asking
yard (peers.md, "Code that comes back"). All of this is keyed by the peer's
key, not its name: a rename moves every name that names the peer in one
write, and `peer rm` ends the link and its grants.

### 2.6. What a signature does not cover, stated plainly

Today, a reader of the wire — the edge, a tunnel or proxy operator, anyone
who can read plain http between two yards, anyone who later reads a log
that kept a body — sees: every mail's text and files, every bead's title,
body and notes, every ask and answer, every file's bytes, and the roster
(names, kinds, one-line roles). They cannot forge, alter or replay a
message past its five minutes, and cannot learn the yard's private key.
Linking itself also crosses in the clear (names, keys, the URL; peers.md,
"What pairing rests on"). The design problem is therefore exactly (b):
keep the first half (nobody forges) while removing the second (everybody
reads). And (a) is the linking problem restated: today there is nothing to
look up, so the key can only arrive in a code read aloud or a line carried
by hand.

## 3. The proposal

**Recommendation: shape 2 — standards on today's link.** Keep the wire,
the outbox, the held pull, the kinds and every rule of §2.5, and change
three things, each to a standard that already exists:

1. **Discovery (a): WebFinger (RFC 7033) served by the edge.** A yard's
   address is an `https` URL on port 443 — `https://my.yardr.dev/<account>/<yard>`
   for a yard at the edge, any such URL a self-hosted yard chooses —
   and that URL itself is the WebFinger resource (RFC 7033 takes any
   URI as its resource): the host of the URL answers
   `GET /.well-known/webfinger?resource=<that URL>`
   with a JSON Resource Descriptor (JRD) carrying the yard's peer endpoint
   (its edge peer URL), its signing key, and its encryption key (§3.3).
   Looking a yard up becomes an HTTPS GET any program can make.
2. **Signing: HTTP Message Signatures (RFC 9421) in place of our
   signature scheme.** The same ed25519 key signs, but the signature base,
   the headers (`Signature` / `Signature-Input`) and the covered components
   (`@method`, `@path`, `@query`, `yardr-peer-key`, `yardr-peer-time`,
   `content-digest`) are the standard's, so a yard's request can be
   verified by a library that has never heard of yardr.
3. **Bodies (b): HPKE (RFC 9180), base mode, sealing each message body
   to the receiver's encryption key.** The request still travels signed
   and in the open as to its headers; the *body* — the message itself —
   crosses as ciphertext that only the receiving yard can open, over the
   edge, over plain http, over any relay. Go's standard library already
   carries HPKE (`crypto/hpke`, `DHKEM(X25519, HKDF-SHA256)` with
   `ChaCha20Poly1305` or AES-GCM) and X25519 (`crypto/ecdh`), so this adds
   no dependency, where shape 1 adds a homeserver and a Matrix stack.

The rest of this section says what each piece is, what a person types and
sees, what is kept and what is replaced, how a yard migrates, and who sees
what. What it costs to be wrong about each open choice is §5; the pieces in
landing order are §6.

### 3.1. Discovery: a name a stranger can look up

Each connected yard already has a name at the edge — its account handle
and its yard name, the two segments of `https://my.yardr.dev/peers/<account>/<yard>`
and of its hook URLs (edge.md, "Hosted and connected"). The proposal makes
that name an address by dropping the one segment that names the
protocol: `https://my.yardr.dev/<account>/<yard>`. No per-account
subdomain is minted, so no wildcard DNS or certificate is needed; the
address's host is the edge's own host, whose TLS is the trust in the
answer (Open Question 1, decided, §5). The edge serves the lookup at
that host, and only for yards whose operator turned findable on
(Open Question 4, decided opt-in, §5; part of bead 2, §6):

```text
GET https://my.yardr.dev/.well-known/webfinger?resource=https%3A%2F%2Fmy.yardr.dev%2Fada%2Flaptop
→ 200 application/jrd+json
{
  "subject": "https://my.yardr.dev/ada/laptop",
  "links": [
    {"rel": "https://yardr.dev/peers/endpoint",
     "href": "https://my.yardr.dev/peers/ada/laptop"},
    {"rel": "https://yardr.dev/peers/signing-key",
     "href": "data:,a8tUnLBerkUE5hhF7C4ejE-GeV12GYuXpfNf1Jw1grY"},
    {"rel": "https://yardr.dev/peers/encryption-key",
     "href": "data:,x25519:..."}
  ]
}
```

(The `rel` values are yardr's own link relations; WebFinger standardises
the query and the JRD, not the relations, and a yardr relation is how a
client knows which link it wants. The key hrefs above are schematic; the
bead that builds this pins their encoding.) Three properties do the work.
First, the lookup is one HTTPS GET to the address URL's host, with the
address URL itself as the resource — WebFinger is usually asked for an
`acct:` URI, but RFC 7033 defines the resource as any URI, and an
`https` URL names its own lookup host, so no naming scheme has to be
invented or delegated — so "look the yard up"
stops being yardr lore (when a yard itself looks a name up, forward or
in reverse, it is the guarded fetch of §3.4; that guard is about the
fetcher, not the record). The endpoint href is part of the record, and
therefore untrusted in the same way: it names where the yard will send
signed requests next, so it is stored and dialled only under the
endpoint policy of §3.4, never merely because a record returned it.
Second, what comes back is exactly what `peer add`
needs today — endpoint, signing key — plus the one new thing, an encryption
key (§3.3), so discovery feeds the stored link of §2.1 unchanged. Third,
trust in the answer is the address host's TLS certificate at lookup time, pinned
afterwards: the yard keeps the keys it got, by key as today, and a later
lookup that disagrees is a changed key, to be refused and shown, not
quietly followed. That pin, at first link, trusts the discovery host
itself: the edge and any host that serves a yard's record are inside the
trust boundary at that moment (§3.7 states what that does and does not
protect against), with an optional six-digit accidental-mismatch
comparison beside it (Q1, decided, §5); the mechanism here needs only a place to store what was found, which
`store.Peer` already is.

A yard with no edge account is still discoverable the same way if it
serves the same JRD itself: the lookup always goes to the *host of the
address URL*, so a yard whose address is `https://north.example.com/laptop`
answers for it at `north.example.com`, whatever path it chose. Any
`https` URL on port 443 can be such an address — the lookup is fetched
at port 443 (§3.4), so an address naming any other explicit port is not
one — and the path is the yard's own. The edge is
the directory for yards that have one; it is not a new root for yards that
do not. The `acct:<yard>@<domain>` form the bead's example used is left
out as the primary form (§4); nothing here forbids serving it later as
an alias of the same record. `did:web` would give the same keys with DID-document machinery, a
resolver, and verification-relationship vocabulary yardr does not
otherwise use; it is left out (§4) because WebFinger carries the three
facts needed and nothing else is bought.

### 3.2. Signing: the standard's headers, the same key

RFC 9421 signs selected components of an HTTP message and carries the
result in `Signature-Input` (what was signed, with which key, when) and
`Signature` (the value). Replacing yardkey's `request()` / `response()`
byte strings with that base, and the three `Yardr-Peer-*` headers with the
standard pair plus `Content-Digest` (RFC 9530) over the body, buys three
things. A peer request can be checked by any RFC 9421 library, which is
the honest form of "use an existing standard": an outside program links
two yards, or audits a link, without yardr's code. The covered-components
list makes explicit what today's string join leaves implicit, including
`@query` for the pull's `after`, `wait` and `refused` parameters, which
today are covered as part of the path string. And the standard has a name
for the timestamp's job (`created`, with `expires`): today's five-minute
`MaxSkew` window becomes a stated validity, checked the same way, so the
sleep/wake replay that gave the "timestamp off" 401s stays fixed by the same means
(a short validity, and no pooled connection to replay on).

The profile is fixed here, because "use RFC 9421" without one would leave
the building bead to invent the part that matters — what is bound to
what. A request carries two new plain headers beside the standard pair:
`Yardr-Peer-To`, the intended receiver's signing public key, exactly as
`yardkey.Public` writes it, and `Yardr-Peer-Nonce`, a fresh value for
this one request (below). `Yardr-Peer-To` carries the binding today's
bespoke string has and the standard's path components cannot:
`request()` in yardkey signs the receiver's key (`to`), so a request
signed for one peer is no request to another, and `Caller.do` passes
`p.Key` for that purpose. Two direct peers share the path
`/peers/messages`, so `@path` and `@query` alone do not name the
destination, and a pull (`GET`, no body) has no ciphertext that could
bind it indirectly. `Yardr-Peer-To` is therefore a covered component
of every request. `Yardr-Peer-Nonce` is likewise covered, so the value
the answer must echo (below) is itself signed by the asker. Both
headers survive the edge because the edge forwards the peer headers
unchanged. The path components need a
rule of their own, and today's do not transfer: today's caller signs
the `path` argument it is handed — the peer-local target, `Path` plus
its query (`Caller.do` in
[caller.go](../../internal/yards/caller.go) signs that argument, not
the URL it actually posts to, `p.URL+path`) — and today's receiver
verifies `r.URL.RequestURI()` as the edge left it, which matches only
because the edge strips its `/peers/<account>/<yard>` prefix before
the yard sees the request (the stand-in edge in
`internal/yards/edge_test.go` does exactly that strip). RFC 9421's
`@path` is not a free choice: it is derived from the actual request
target (RFC 9421 §2.2.6), so a sender that posts to
`https://my.yardr.dev/peers/<account>/<yard>/peers/messages` derives
that full path, and a receiver handed only the stripped remainder
derives a different one and refuses a good signature. The profile
therefore signs what is sent: the sender derives `@path` and `@query`
from the URL it actually sends to — `/peers/messages` direct, the full
`/peers/<account>/<yard>/peers/messages` through the edge — and, on
peer routes, the edge forwards that path unstripped, so the request
target the receiver sees is the one that was signed. The receiver
accepts the path its own address URL gives it: it routes on the
`/peers/messages` (or `/peers/link`) ending of the target it received,
and derives `@path` and `@query` from that whole received target, not
from a stripped remainder. Changing the edge's rewrite this way, and
its test, is part of the yardr-hosted work (§6, bead 3, which also
forwards the link route), and the trial signs and verifies through the
edge so the two derivations are proven equal there (§6, bead 5). A request
whose `Yardr-Peer-To` is not the verifying yard's own key is refused
before any kind is considered: it was signed for someone else, however
valid its signature.

In full, a request's `Signature-Input` names covered components
`("@method" "@path" "@query" "yardr-peer-to" "yardr-peer-nonce"
"content-digest")` for a `POST` and `("@method" "@path" "@query"
"yardr-peer-to" "yardr-peer-nonce")` for the bodyless `GET`, with
parameters `keyid` (the sender's signing public key, as
`Yardr-Peer-Key` names it today), `alg="ed25519"`, `created` (Unix
seconds) and `expires` (`created` plus 300 seconds, today's `MaxSkew`
stated as a validity) and `tag="yardr-peer-req"`. The freshness value
is not a signature parameter but the header itself: `Yardr-Peer-Nonce`
carries a fresh random value (at least 128 bits, written as an
unpadded base64url string) minted for this one request and never
reused — the value the answer echoes to prove it answers this request
and no earlier one (below). A request re-sent byte for byte is the
same request; any new attempt, a link check included, mints a new
nonce header. `Content-Digest` is
`sha-256` over the body as it crosses — after §3.3, the HPKE envelope
bytes — so the signature authenticates the ciphertext; the plaintext is
checked once opened. On the messages route (`POST`/`GET
/peers/messages`) the receiver verifies from headers alone before it
reads a large body. That is possible only because RFC 9421 covers the
`Content-Digest` header, not the body bytes — today's yardkey
signature covers the body's sha256, so verification there needs the
whole body first (`internal/yards/receiver.go` `verify`, which chooses
its read limit from the unverified `Yardr-Peer-Key` header and
verifies after the read). The new order is: read no body past
`maxRequest` (`MaxPeerBody` + 4 KiB) yet; parse `keyid` unverified to
find the link (`PeerByKey`, as it reads `Yardr-Peer-Key` today) and
refuse 401 plain when no peer has that key; check `Yardr-Peer-To`
against its own key; check `created`/`expires` against its clock; and
verify the signature from the headers alone — the covered components,
`keyid`, `created`/`expires` and the `Content-Digest` header value.
Only when that holds does the receiver grant the file-sized read
limit (§3.3), read the body up to that bound, and check the body
against the signed digest (a mismatch is refused before opening). A
request that fails any of those header checks is refused 401 having
read nothing past `maxRequest`. An unknown key on that route is
still refused before any kind is considered; nothing about the link
route below weakens it. The one exception to that lookup is
`POST /peers/link` itself, whose sender is by definition no peer yet:
it is verified by the bootstrap order in §3.4 (possession of the
claimed key, then a reverse lookup), never by `PeerByKey`, and the
handler dispatches on the route before any peer lookup so the two
orders cannot be confused. An answer is signed as an RFC 9421
response, following §2.4 of the RFC: it does not cover the request's
`Signature` field — §7.3.7 of the RFC warns against exactly that,
because a response bound only to a signature value can be replayed
for a different request whose signature happens to collide or be
reused — and it does not cover `@signature-params` either, which
§2.3 of the RFC makes the mandatory final line of every signature
base and forbids enumerating as a covered component. Instead it
covers components of the request itself, each named in the
covered-components list with the standard's `req` component
parameter, together with the answer's own status, body hash and
echoed nonce. The answer covers every component the request itself
covered, each with `;req` — RFC 9421 §2.4's SHOULD, taken whole, with
no deviation to explain — beside its own echoed nonce. The answer
carries `Yardr-Peer-Nonce` with exactly the value the request
carried, and that header is a covered component of the answer in two
distinct forms: as the answer's own `yardr-peer-nonce` (the echoed
value) and as `"yardr-peer-nonce";req` (the request's value it
answers). In full, the answer's covered components are
`("@status" "content-digest" "yardr-peer-nonce" "@method";req
"@authority";req "@path";req "yardr-peer-to";req
"yardr-peer-nonce";req)`, plus `"@query";req` for a request that
carried a query and `"content-digest";req` for one that carried a
body, with parameters `keyid` (the answering yard's key) and
`tag="yardr-peer-res"`. Freshness is
bound by the echo, not by covering any signature parameter: the
asker verifies the answer by re-deriving the `;req` components from
the request it just sent and by checking that the answer's covered
`yardr-peer-nonce` equals the fresh value on that request, so an
answer made for any other request, however valid its own signature,
does not verify for this one, and replaying an earlier answer to a
later attempt fails because its echoed nonce is the earlier
request's, not the new one. That is today's `response()` binding —
an answer tied to one particular request, not to a class of them —
in the standard's own encoding; the only yardr header it needs is
the nonce header both sides already cover. The caller checks the
answer's `keyid` is the peer it asked, `@status` and the digest
match, the covered request components are those of the request it
just sent, and the echoed nonce is that request's, exactly as
`VerifyResponse` binds an answer to one request today. A `/peers/link`
answer is signed the same way, and the asker checks it against the
asked yard's key it pinned by its forward lookup (the record it stored
at `peer link` time), not against a peer the asked yard is yet known
by. The edge's capped
hold (204, `X-Edge-Hold: capped`, no body) keeps having no signature
surface and is still asked again, not cried hostile over.

What the standard does *not* do, and the design must not pretend it does,
is distribute keys: RFC 9421 leaves key discovery to the application. In
this proposal that is §3.1's JRD (and, for a yard added by hand, the invite
line carrying the same key as today). The signing key does not change: it
is the yard's ed25519 `peer.key`. This is a change of encoding, not of
trust: same key, same store lookup, same refusal shapes, new bytes on the
wire. Because it is breaking (§3.6), it lands with the body change in one
bead, not beside it as a second flag day (§6).

### 3.3. Bodies: sealed to the receiver, readable by nobody between

Each yard gains a second key pair: an X25519 encryption pair beside
`peer.key` (a `peer-enc.key` in the yard's home, made once the same way,
mode 0600). Its public half is published wherever the signing key is: in
the JRD (§3.1) and in the invite line's next version, and it is stored on
the link when the link is made (`store.Peer` gains the peer's encryption
public key beside its signing `Key`). A second pair, not a conversion of
the ed25519 pair: an ed25519 key can be mapped to Curve25519, and the
yardmaster's 25 Oct note raised exactly that, but Go's standard library
exposes no such conversion, the mapping's clamping and cofactor edge
cases are a known trap, and a separate pair keeps the signing key's job
singular. The cost is one more file to back up, stated in §3.6. The
encryption key does not rotate in this proposal (§4): a peer seals only
to the key pinned on its stored link. There is no way for a stored link
to gain that key from anything the peer says: `PeerSays` keeps carrying
only what it carries today (`understands`, `yardr`, `roster`), no clear
key advertisement is added, and no bootstrap exchange is invented to
teach one. A link that has no encryption key pinned — every link made
before the flag day is one (§3.6) — is not usable for sealed traffic at
all, and changing the key on purpose is a re-link, not an exchange. A
later lookup that disagrees with a key already pinned is a changed key:
refused and shown, never quietly followed (§3.1).

Sending is then one step in `Caller.Push`, one in the pull's answer and
one in the receiver's signed answer to a push, and taking is the mirror
of each: the JSON body that today crosses whole is sealed with HPKE base
mode to the receiver's encryption public key, through the context API
— `hpke.NewSender` with `DHKEM(X25519)`, `HKDF-SHA256` and
`ChaCha20Poly1305`, then `Sender.Seal(aad, plaintext)`, and on the
receiving side `hpke.NewRecipient` with `Recipient.Open(aad,
ciphertext)` — because the sender and receiver signing keys are bound
in as additional authenticated data (below), and the library's
top-level `hpke.Seal` helper takes no such data and so cannot carry
that binding. What crosses
instead is a small JSON envelope in the clear — `{"v": 2, "enc":
"<encapsulated key>", "ct": "<ciphertext>"}` — both values
base64url without padding (the `yardkey` encoding), because JSON
cannot carry raw bytes, whose plaintext, once
opened, is exactly today's body: for a push, the `PeerMessage` with its
`says`; for a pull, the whole `PeerPage` sealed as one blob to the
asker; for a signed answer, the taken/refused fields with their `says`.
The receiver must be able to open the envelope before it can know
anything inside it, so nothing inside selects how it is opened: the
HPKE `info` is the protocol alone, the constant bytes
`yardr-peer-hpke-1`, the same for every kind and every yard, and the
additional authenticated data is those three lines joined by a newline —
`yardr-peer-hpke-1`, the sender's signing key, the receiver's signing
key — all known to the receiver from the verified signature and its own
key before opening. The message id and kind stay *inside* the seal exactly
as today, so a carrier cannot read the kind from the envelope (§3.7);
the direction is bound by the key order in the additional data, so an
answer's ciphertext is no push's. The headers stay in the clear and stay
signed: method, path, the signer's key, the time, the intended receiver
(§3.2). The order on taking does not change in shape: verify the
signature over the envelope, open the envelope, then run every §2.5
check — kinds, roster, caps, refusal reasons, id-once dedupe — on the
opened body exactly as today, because the opened body *is* today's body.
An envelope that does not open (wrong key, tampered bytes) is a bad
message, refused as one is today after the signature has held; it is
never retried as cleartext.

The seal costs wire bytes, and the limits have to say so without moving
the limits that mean something. The plaintext bounds do not change: a
file is still at most the yard's `peer_file_max_bytes` (at most 32 MiB,
`MaxPeerFile`), a file message's plaintext body at most
`MaxPeerFileBody` (that file in base64 plus the name), and any other
body at most `MaxPeerBody` (256 KiB). But a file body already carries
its bytes in base64, and the envelope base64-encodes the ciphertext a
second time, so the largest body grows by roughly a third again on the
wire: a 44,743,340-byte plaintext file body (44.74 MB, 42.67 MiB)
becomes about 59.66 MB (56.89 MiB)
of envelope (the ciphertext is the plaintext plus HPKE's 16-byte tag;
`ct` is its base64, `enc` adds 43 characters for the 32-byte
encapsulated key in unpadded base64url, and the envelope's own syntax a few dozen more).
Neither of today's wire caps fits that: the receiver reads a request
at most `MaxPeerFileBody` + 4 KiB (`maxFileRequest`,
[receiver.go](../../internal/yards/receiver.go)) and the caller reads
an answer at most `MaxPeerFileBody` + 1 MiB (`maxAnswer`,
[caller.go](../../internal/yards/caller.go)). Bead 1 (§6) therefore
derives both from a sealed bound — the envelope encoding of
`MaxPeerFileBody`, computed from that constant and not a new magic
number — keeping each cap's existing slack (4 KiB on a request, 1 MiB
on an answer, the latter for the pull page's framing around its one
file message). Because the kind is inside the seal, the receiver
cannot choose its read limit by kind — and on the new wire it does
not choose it by the unverified `keyid` either. Today it does exactly
that: `verify` in [receiver.go](../../internal/yards/receiver.go)
grants `maxFileRequest` to any request whose `Yardr-Peer-Key` header
names a known peer, reads the body, and verifies only afterwards, so
anyone who knows a peer's public key can make the yard read megabytes
before a 401. RFC 9421 removes the reason: the signature covers the
`Content-Digest` header, not the body, so the receiver verifies from
headers alone first, exactly as §3.2 states — `keyid` to find the
peer, `Yardr-Peer-To`, `created`/`expires`, the covered components
and the `Content-Digest` header value — and grants the file-sized
limit (the sealed bound above) only after that holds. It then reads
the body up to that bound and checks it against the signed digest
before opening. A request whose header signature fails, whose key is
unknown, whose `Yardr-Peer-To` names another yard, or whose validity
has expired is refused 401 having read nothing past `maxRequest`
(`MaxPeerBody` + 4 KiB). The wire cap is a transport ceiling only:
a body over the sealed bound is refused 413 before opening, and a body
that opens to more than its plaintext bound, or to a file over the
yard's setting, is still refused `too_large` after opening. No file
that is refused today becomes acceptable; it only becomes possible to
carry an acceptable one sealed.

Four consequences, stated plainly:

- **Any path becomes private.** The edge, a proxy, plain http: each sees
  headers and ciphertext. The sentence in peers.md that tells the operator
  to put TLS in front of the listener for anything private goes away; TLS
  becomes optional defence in depth, not the privacy mechanism. This is
  (b) whole.
- **Base mode has no forward secrecy.** Whoever later gets a yard's
  encryption private key can open every body ever sealed to it that they
  recorded. That is the known price of one static receiver key (the same
  price `age`'s X25519 recipients and a sealed box pay), and it is named
  here rather than discovered later. Paying more — signed
  ephemeral prekeys, a ratchet — was Open Question 3 and is decided
  against for now (§5); nothing in the
  envelope forbids adding a prekey field later.
- **A file is sealed the same way.** Today's `file` body is base64 bytes
  in one message; it seals as any body does, and the plaintext caps —
  the file's own size and its message body's — stand exactly as above.
  HPKE encrypts in one shot here, so no chunked construction is
  invented; only the wire caps grow, by the second base64 the envelope
  costs, and §6 bead 1 proves a near-limit file crosses both ways.
- **The link ask itself is not sealed, and needs no seal.** The
  `POST /peers/link` body (§3.4) carries only what each yard publishes to
  be found — its address, name and two public keys, and where it can be
  called — signed, over whatever path, exactly as the pairing transcript
  (names, keys, URL) crosses in the clear today. No message body, roster
  name or bead is in it. Discovery-led linking needs no pairing exchange
  at all: the keys arrive by lookup, and the first signed, sealed ping
  after approval proves the link (§3.4's gate holds the asker's until
  then).

`age` was the other candidate for this step, and it loses on one point:
`age` is a file-encryption format (header, stanza, chunked payload
stream), excellent at rest, but a message body here wants a one-shot
sealed box with protocol context bound in, which is HPKE's base mode
exactly, in the standard library, with test vectors from the RFC. Using
`age` would adopt its recipient stanza and then bend the format to a use
it does not name. HPKE is the existing mechanism that already fits.

### 3.4. What a person types and sees

Linking by address cannot ride the machinery a made link already has,
and this section does not pretend it does. Today the receiver serves
only peer messages (`POST`/`GET /peers/messages`, each verified against
a peer the store already has — an unknown key is 401 before any kind is
considered) and the three pairing routes, which are the only doors a
yard that is no peer yet can knock on. A peer `ask` travels over a made
link as a message kind, and `peer add` and a pairing are the operator's
alone, typed in a terminal and refused as a run ask — so a request that
arrives before any link exists is neither, and needs a door of its own.

That door is one new route beside the messages: `POST /peers/link`, on
the same listener (and forwarded by the edge under the same base), whose
body names the yard that asks: its own findable address
(`https://my.yardr.dev/hal/desk` below), what it calls itself, its signing key,
its encryption key, and where it can be called (empty for a yard that
only pulls). The request carries the §3.2 signature profile —
`keyid` its own signing key, `Yardr-Peer-To` the asked yard's — but it
cannot be verified by that section's messages-route order, because
there is no peer to look up yet. The handler therefore dispatches on
the route first, and `/peers/link` is verified in this route-specific
order, none of which consults `PeerByKey`:

1. The body is read at most 4 KiB and parsed untrusted: the address,
   name, two keys and endpoint it claims, and the signature parameters.
   Anything malformed, oversized or missing a covered component is
   refused plain (400) and counted; no record is made.
2. `Yardr-Peer-To` must name the asked yard's own signing public key
   exactly as `yardkey.Public` writes it. A request signed for another
   yard is refused plain (401) here, before any lookup is spent on it.
3. Possession is proved with the claimed key itself: the RFC 9421
   signature is verified using the key `keyid` names as the
   verification key, over `@method`, `@path` as sent (the URL the
   asker actually posted to — `/peers/link` direct, the full
   `/peers/<account>/<yard>/peers/link` through the edge, forwarded
   unstripped and accepted as the receiver's own address URL gives it,
   as in §3.2),
   `@query`, `yardr-peer-to`, `yardr-peer-nonce` and `content-digest`,
   and `created` / `expires` must hold the same 300-second window. A failure is a
   plain 401. This proves the sender holds the private key for the
   signing key in its body; it proves nothing about the name yet, and
   no identity is accepted at this step.
4. Cheap store checks come next, still before any network fetch: the
   claimed key is not this yard's own; a key that is a peer already is
   answered as `linked` (below) rather than kept again, and one inside
   a decline's day as `declined` — in both cases the identity was
   accepted when that link or decline was made, so no new lookup is
   owed. The same holds for a live pending ask whose address, signing
   key, encryption key and endpoint all equal the pending record's:
   it is refreshed and answered `pending`, because that exact claim
   already passed step 5. A claim that differs in any of those fields
   from the record it would touch is a new claim and goes to step 5.
5. Only then is the name checked, by looking the claimed address up
   (the same WebFinger GET of §3.1, in reverse — the claimed URL as
   resource, at that URL's host, through the guarded
   client below) and requiring all three to match: the record's
   `subject` is exactly the claimed address URL, its signing key
   is the key whose possession step 3 just proved and which the body
   claims, its encryption key is the body's, and its endpoint is the
   body's (empty on both sides for a yard that only pulls). Any
   disagreement, unreachable lookup or exhausted quota creates no
   record and is counted, as pairing refusals are.

An attacker must then control the address URL's host (or its edge
account) *and* the signing key to ask in that name. That is the same
root the forward lookup already trusts. An optional `peer verify`
static fingerprint stands beside that pin (Q1 decided, §5). It is
an accidental-mismatch check only. A yard with no findable address
of its own cannot ask this way and is told so; the invite line below
remains its way in. A kept ask is answered `202 {"state": "pending",
"expires": …}`, signed as §3.2 signs an answer, so the asker — which
already pinned the asked yard's key by its forward lookup — can verify
it reached that yard; a repeat ask for a link already made is answered
`{"state": "linked"}`, and one inside a decline's day `403 {"state":
"declined"}` on the same signed footing. Only the asked yard's
operator, in a terminal there, moves a pending ask past pending
(below); no answer a requester can solicit approves its own link. The
ordinary `/peers/messages` route keeps §3.2's order throughout: an
unknown key is refused there before any kind is considered, and
nothing in this bootstrap lends it a sender.

The reverse lookup is a fetch the asker chooses the target of, made
before any operator has approved anything, and a signing key costs
nothing to mint — so possession in step 3 is a cheap ticket, not a
reason to fetch freely. Both lookups (this one, and the forward one an
operator starts) go through one guarded client, whose rules are part
of the route, not hardening to be added later:

- The target is built by the looking-up yard, never taken from the
  request beyond the one thing being checked: the claimed address must
  itself be an `https` URL (no other scheme, no userinfo, no
  fragment), the host is that URL's host and nothing else in the body
  can move it, and the path is the fixed `/.well-known/webfinger` on
  that host with `resource=<the claimed address URL>`, at port 443
  (no other explicit port in the lookup URL). The host must be a public fully
  qualified domain name — at least two DNS labels, lowercased to its
  ASCII (IDNA) form — and never an IP literal, `localhost`, a single
  label, or a name under `.local`, `.localhost`, `.internal` or
  `.home.arpa`.
- Every address the host resolves to is checked before anything is
  dialled, against an allowlist, not a blocklist: an address passes
  only if it is globally routable unicast in the IANA special-purpose
  registries' sense (marked globally reachable), and the lookup fails
  whole if any A or AAAA record is not. Loopback, private
  (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, fc00::/7), link-local,
  multicast and unspecified addresses fail, and so do the
  special-purpose ranges a blocklist forgets: carrier-grade NAT
  (100.64.0.0/10), the documentation nets (192.0.2.0/24,
  198.51.100.0/24, 203.0.113.0/24, 2001:db8::/32), benchmarking
  (198.18.0.0/15), 0.0.0.0/8, 192.0.0.0/24 and 240.0.0.0/4, the
  IPv4-translation prefixes (64:ff9b::/96 and 64:ff9b:1::/48, whose
  embedded IPv4 address could name a private service in translation),
  and every
  other range its registry marks not globally reachable. The check
  refuses known private and special-use ranges; it does not and cannot
  prove that a globally numbered address is not routed to an internal
  service on this yard's own network, so the design assumes the yard's
  network does not do that — an operator whose network does route
  global addresses inward does not turn findable on and links by
  invite, which fetches nothing. (The standard
  library's `IsGlobalUnicast` does not state this — it counts private
  addresses as global — so the check is an explicit prefix table with
  those cases in its tests, not that one call.) The yard then dials a
  validated address literally, with the original host as the TLS
  server name and certificate name and no second resolution in the
  dialler, so a name cannot answer safely at check time and rebind to
  an internal address at dial time; the fetch goes direct, with no
  proxy from the environment re-resolving it elsewhere.
- At most three redirects are followed, each only to another `https`
  URL whose host passes the same name and address checks before it is
  dialled; a redirect to `http`, to an IP literal or to a host whose
  addresses fail those checks ends the lookup. The whole fetch — resolution, redirects,
  body — has five seconds; the response is at most 32 KiB, must claim
  `application/jrd+json` (or JSON), and only its `subject` and `links`
  are read.
- The lookup is quota'd before it is made, because the pending cap
  below bounds only successes: results are cached for five minutes,
  failures included, keyed by the full normalised resource URL (the
  whole address, host and path together, normalised as the lookup
  normalises it) — never by host or account alone, since one account
  holds many yards and one yard's record must never answer for
  another's; at most 5 lookups
  per claimed host per hour, 5 per claimed signing key per hour, 20
  per remote address per hour where that address is the asker's own
  (through the edge it is the edge's, which is why the host and key
  quotas are the load-bearing ones), and 60 per yard per hour in all.
  The same counters count a link request answered from the cache or
  from a live pending record, so a shortcut is not a way around them,
  and refreshing a pending ask does not move its seven-day expiry.
  Past a quota the answer is a plain 429, no fetch is made, and the
  refusal is counted like any other. What this leaves an attacker is
  named in §3.7.

Guarding the lookup alone would still leave the record it returns free
to aim the yard. The endpoint href is where every later push, pull and
link check goes (`Caller.do` builds each request from the stored peer
URL), so a hostile or compromised domain could answer its own
WebFinger correctly and name `http://127.0.0.1:…`, a LAN service, or a
name that resolves privately on the second look — and the equality
check in step 5 above, which only proves the claimant and the record
agree, would wave it through. The endpoint therefore passes the same
guard, twice over:

- *At storage.* An endpoint taken from a record — by the asking yard
  from its forward lookup before it stores the peer, and by the asked
  yard from the reverse record before it keeps a pending ask — is
  refused unless: it parses under the same rule a peer URL parses under
  today (`yardkey.CleanURL`: no userinfo, query or fragment), its
  scheme is `https`, its host is a public fully qualified domain name
  under the host rules for lookups above (never an IP literal), and
  every address that host resolves to at that moment passes the
  allowlist above. An empty endpoint (a yard that only pulls) needs no
  check and stores no URL, as today. A refused endpoint makes `peer
  link` fail with the reason and stores nothing; in reverse, it makes
  no pending record. The stored link remembers that its URL came from
  a lookup, which `peer show` already has the words for ("by address"),
  so the dial check below knows which URLs it owns. An operator who
  later sets the URL themselves (`peer set` / `SetPeerURL`, or a peer
  added by invite) takes it out of this policy: that URL was typed,
  not discovered, and a deliberate LAN address (`http://10.…`, the
  invite example) stays possible exactly there, at the operator's
  word, and nowhere by discovery.
- *At every dial.* Storage checked the answer once; DNS can change.
  Every call the dispatcher or a link check makes to a
  lookup-stored URL resolves the host again, requires every answer to
  pass the same allowlist again, and dials a validated address
  literally with the original host as TLS name, direct, with no
  proxy — the lookup fetch's dial, reused for peer calls, so a name
  that rebinds between link and dial is refused as a failed try,
  never dialled. A peer endpoint's redirect is never followed: the
  peer caller today follows whatever its HTTP client follows, and
  under this policy its client refuses redirects outright, so a 3xx
  from an endpoint is a failed call, counted like any unreachable
  answer, and no second address is dialled unchecked. (The lookup
  fetch's three redirects, each re-validated before dialling, are the
  only redirects followed anywhere in this design.)

What survives is a *pending link*, not a peer: a durable record in the
store (a pairing lives only in the server's memory because its code
expires in minutes; an approval may wait days, so this one is kept
across restarts), keyed by the asker's signing key, holding the
address, name, both keys, endpoint and when it was asked, expiring
after seven days, at most 32 held, a repeat from the same key and
address refreshing the same record rather than stacking. It grants
nothing: no kind, no roster, no outbox. It is the operator's alone,
like `peer add` — it is never a peer message, never a run ask, and no
reach can approve it. The asked yard's operator sees it in a terminal
and answers there:

```text
# on desk: link with the yard at ada/laptop
$ yardr peer link https://my.yardr.dev/ada/laptop
looked up https://my.yardr.dev/ada/laptop: endpoint https://my.yardr.dev/peers/ada/laptop
  signing key a8tU…grY · encryption key 7f3…Qe (pinned on save)
saved as peer laptop: the link will carry ping both ways and nothing else,
  and nothing of this yard is exposed to it
asked laptop to approve this yard as https://my.yardr.dev/hal/desk: pending there
  (awaiting approval: this yard sends laptop nothing until then;
   yardr peer show laptop says so)

# on laptop: the pending ask, in a terminal there
$ yardr peer requests
https://my.yardr.dev/hal/desk  calls itself desk · signing a8tU… · encryption 9c1… ·
  called at https://my.yardr.dev/peers/hal/desk · asked 2026-10-26 09:14 · expires in 7 days
$ yardr peer approve https://my.yardr.dev/hal/desk
peer desk approved: the link carries ping both ways and nothing else
$ yardr peer ping desk
pong from desk in 212ms: the ping went by push, the pong came by pull, both sealed
```

When each side stores the link, and what it grants, is now exact. The
asking yard stores the peer at `peer link` time, from its forward
lookup — name as the operator names it (for an edge address, its last
path segment by default), both keys pinned, the endpoint as its URL — with `ping`
allowed each way and nothing exposed, the same first grant a pairing
saves. That record is stored *awaiting approval*, and the gate is on
the asking yard's own sending, not on the asked yard's refusal: while
a peer is awaiting approval the dispatcher does not call it at all —
no push of a queued message, no pull, however many passes go by — so
nothing queued for it is attempted, no try is counted, no backoff
starts, and nothing can be given up (today's give-up after 24 tries,
most of a day, would otherwise burn a ping queued on day one of a
seven-day wait). Messages for it wait in the outbox as untouched
plaintext in the sender's own store, exactly as §3.6's queued messages
do, and go in sequence once the gate clears. The gate is outbound
only: the asking yard still takes a signed push from that peer by its
stored record, which is how approval first shows itself (below). A
peer made by hand (`peer add`) or in any other way is never gated;
only the asking side of a `peer link` starts this way, and the asked
side never does. The asked yard
stores nothing but the pending record until its operator approves;
approval writes the peer (the name the asker gave itself, or the
operator's `--name`; its two keys; its endpoint, or none where it only
pulls) with the same `ping`-only grant, deletes the pending record, and
logs `peer_added` and `peer_allowed` as an add does. Declining
(`yardr peer decline <address>`) deletes the pending record and keeps a
day's tombstone, so a repeated `peer link` from the asking side is told
it was declined rather than held pending for ever; the asking operator
then removes their one-sided record with `peer rm`, as with any link
that will not be made.

The asking side's gate clears only on proof the approval happened,
and nothing polls for it — the yard never polls, and this link state
is no exception. Proof is either inbound: any signed message or answer
heard from that peer under its pinned key (the approver's first push
across, or a pull answered after approval) clears the gate as it
records the heard-from time, because the asked yard could only sign
as a peer once it had stored the link. Or it is checked: running
`yardr peer link <the same address>` again on a gated peer, or
`yardr peer ping` on one, sends no `/peers/messages` at all, but asks
`POST /peers/link` once more — the asked yard answers `linked` if it
has approved (its stored-peer check in step 4 above), `declined`
inside the tombstone's day, or `pending` while it still waits, each
signed as §3.2 signs an answer and checked against the pin — the
answer covering this check's own request components with `;req` and
echoing this check's fresh `Yardr-Peer-Nonce` under its own covered
`yardr-peer-nonce` (§3.2), so a `linked` answer recorded from an
earlier check, whose echoed nonce is that check's and not this
one's, or made for any other request, does not verify for this one
and clears nothing. `linked`
clears the gate (and a `ping` that cleared it then queues its ping
like any other); `declined` leaves the peer gated and shows as
declined, for `peer rm`; `pending` leaves it gated as it was. If the
pending record has expired (seven days), the check is simply a fresh
ask and starts a new one. An operator who is told out of band "you are
approved" therefore has one command to learn it with, and an approver
whose first act is a ping needs none: the push itself is the proof.
Where the asker only pulls, the approver's messages wait in its own
outbox for the asker's pull, and the asker's check is what first lets
that pull run again. Approval is the only store either way: widening
past `ping`, exposing a name, trust, reach and intakes all stay later
`peer allow` commands by each operator for their own yard, exactly as
now. `peer show` on either side says how the link was made and what is
pinned (`by address, looked up 2026-…, keys pinned`; on the asking side
before approval, `asked …, awaiting approval: nothing sent yet`, which a
checked `linked` or the first heard-from time clears, and a checked
decline replaces with `declined`), and `doctor` keeps its peers line, now able to say
"its address lookup now returns a different key" when a re-lookup
disagrees with the pin, and to count pending asks awaiting the operator.

By hand does not disappear; it shrinks to the fallback it already is in
spirit, for a yard with no findable address to ask from, or one that
cannot be looked up. The invite line gains the encryption key (its next
version, `yardr-peer-2` carrying both keys), and `peer add` keeps
taking an invite as today — `peer link` is the address verb, so the two
ways in stay two commands, each bringing the same two keys to the same
stored link. The eight-character code and the PBKDF2 round count exist
to move a key through a voice call; where a name can be looked up they
have no work left, and §3.6 removes the pairing exchange rather than
carrying it beside the lookup forever (the optional static-fingerprint
`peer verify` beside pinning is decided, Q1, §5; the
code-as-key-transport goes either way).

### 3.5. What is kept, what is replaced

Kept, unchanged in semantics: the stored link keyed by signing public
key; the outbox, sequence numbers, id-once taking, ordering, retries
and give-up; push and the held pull, including the edge's capped hold;
the six kinds and the refusal reasons; the roster, trust, reach,
identities, intakes; the rate limits; `PeerSays` and the two-builds
rules; beads through a git remote; the operator-only gates and the
run-ask approvals for a made link; every event named in peers.md.
Replaced, whole: the signature encoding (yardkey's byte strings → the
RFC 9421 profile of §3.2); bodies in the clear (every message body, pull
page and signed answer body → one HPKE seal each); key transport by
code or carried line (→ WebFinger lookup and pin over the one new
`POST /peers/link` route with its pending record, §3.4, with the invite
line as fallback); and the docs' instruction to bring your own TLS for
privacy (→ sealed over any path). Added and nothing more: the second
key pair, the `enc` on the stored link, the wire caps derived from the
sealed bound (§3.3), the edge enrolment record's two new values — the
yard's encryption public key (`enc`, presented by the yard in bead 1,
§6) and its findable opt-in (`findable`, a boolean true only when the
operator turned findable on, presented by the yard in bead 4, §6) —
both accepted and stored by the edge in bead 2 (§6), the
pending-link record and its three terminal
verbs, the endpoint policy for lookup-stored URLs (checked at storage
and at every dial, §3.4), and the asking side's awaiting-approval gate
(§3.4) — the one
place the dispatcher's behaviour changes, and only to *not* send where
before it would have been refused. Not replaced and not rebuilt: store-and-forward.
The hard part from 25 Oct stays where it already works — in the sender's
outbox and the held question — and no homeserver, mailbox or queue is
added anywhere.

### 3.6. Migration: one breaking change, no compatibility path

Per the 11 Oct decision, this lands as one breaking change of the peer
wire, taken once, with no dual-stack period and no release that reads
both signature schemes (that question is settled by the decision and is
not re-asked in §5):

- The wire changes in a single bead (§6, bead 1): `Yardr-Peer-Key`,
  `Yardr-Peer-Time` and `Yardr-Peer-Signature` are replaced by
  `Signature` / `Signature-Input` / `Content-Digest` /
  `Yardr-Peer-To` / `Yardr-Peer-Nonce` (answers bind the request by
  its `;req` covered components and by echoing its fresh nonce
  header under cover, §3.2), and every body
  crosses as an HPKE envelope. Neither side reaches kind handling when
  the other is on the old build, and neither ever falls back to
  cleartext. A new caller at an old receiver sends no
  `Yardr-Peer-Key`, so the old receiver finds no peer and answers its
  plain unsigned 401 (`unknown_peer`) before it reads a kind; the
  caller, expecting an RFC 9421 answer bound to its request, treats
  that unsigned text as no answer at all. An old caller at a new
  receiver is the mirror: no `keyid` the new receiver can look up, the
  same plain 401, and an old caller that finds no `Yardr-Peer-Signature`
  on the answer it gets. That — a link that suddenly answers only
  unsigned 401s in both directions — is the flag day's whole symptom,
  and it is recognisable: `yardr peer ping <peer>` fails as unsigned on
  the upgraded side, `peer show` still names the peer's last-heard build
  in its `says` (`yardr` older than this build), and the receiver's log
  counts `unknown_peer` where it used to take messages. What happens
  next depends on how long the two builds are apart, and the earlier
  wording of this design — upgrade in any order and queued messages
  simply resume — was wrong past a horizon, so it is stated exactly
  here. For a link that already pins an encryption key — every link
  made on the new wire, and a pre-flag-day link once it has been
  re-linked (next bullet) — the signing key and the stored link never
  change, so a peer that merely upgrades late needs no re-link, and
  `peer show` on a link whose peer was last heard on the old wire says
  so in words ("its build signs the old way: the link waits for its
  upgrade"), which `doctor` repeats, so an operator is never left
  reading a bare 401 as a lost link. A pre-flag-day link is the one
  exception: it pins no encryption key, so upgrading alone does not
  restore it, however long the two yards then agree on the wire. While a
  queued message still has tries left, upgrading the other yard is
  the whole recovery: the message, kept as plaintext in the sender's
  own store and sealed only as each push is made, is carried on its
  next tries. But each failed push across the pause burns one of its
  tries — 24 in all, fewer where a bead's group set its own, with the
  doubling backoff of §2.4, most of a day end to end — and a message
  that runs out during the pause is given up for good (§2.4): marked
  failed, skipped by the outbox and by every later pull handover, its
  file bytes (if any) already stripped, its writer, bead or asker
  already told it was not delivered. Upgrading the other yard after
  that resends nothing, and this design resurrects nothing (§4):
  `peer show` counts what was given up, as it does today, and the
  operator re-originates what still matters — a new mail, a bead sent
  again through the `undelivered` outcome its flow already has, a
  fresh ask. Messages that only ever waited to be pulled are the
  exception that does resume after any gap: no try burns while a yard
  sleeps, so they cross on its first pull after it upgrades. That
  try-burning is the risk of a link that already pins an encryption
  key while its peer answers on the old wire; a keyless pre-flag-day
  link burns no try at all, because it is refused before any dial
  (next bullet) and waits until its operator re-links it. Q5 is
  therefore decided (§5) for crossing inside one announced
  window, shorter than the give-up horizon, instead of trusting any
  order over any gap — with the limit stated there: a message already
  near its final try, or under a lower bead limit, can still run out
  inside that window and is re-originated, not saved by it. That
  window is also a deployment, not a bead landing alone: bead 1
  changes what a yard sends, and bead 3 (§6) changes the path the
  edge forwards it on, and neither half is safe without the other —
  a yard on the new wire calling through an edge that still strips
  its prefix derives a different `@path` than it signed (§3.2), and
  an unstripped edge in front of yards on the old wire hands the old
  receiver a request target its old signature never covered. A
  landing of yardr, further, upgrades the yard it lands on, so bead 1
  must not merge before the window at all. The order §6 states is
  therefore the migration: bead 2 lands first, so the edge can keep
  an encryption key and re-enrolment is available; bead 3 merges
  next but is not deployed — a landing never redeploys the live
  router — and bead 1 waits at `approved` until the window. In the
  window itself, the operator deploys bead 3's edge, merges bead 1
  (the connected yards upgrade), and each yard re-enrols and
  re-links in that same sitting (next bullets); a yard upgraded
  later, outside the window, re-links and re-enrols then in the same
  way, with no recovery command behind it (Q5, §5).
- Each yard makes its encryption pair on first run of the new build
  (`peer-enc.key` beside `peer.key`). A stored link made before the
  flag day has no encryption key for its peer, and it does not gain one
  by upgrading: existing links re-link at the flag day. Until they do,
  the new wire refuses the old record — the caller and the dispatcher
  make no sealed push, pull or ping to a peer whose stored record has
  no encryption key, burn no try and dial nothing, and `yardr peer
  ping`, `peer show` and `doctor` say so in one line ("<peer> was
  linked before sealed bodies: re-link it — `yardr peer rm <peer>`,
  then link it again by its address or its new invite"), which the
  receiver's log counts beside the unsigned 401s above. No `PeerSays`
  field, clear key advertisement or bootstrap exchange teaches the key
  instead (§3.3): a pull could carry the asker's key to the responder,
  but the responder's sealed answer could not be opened by an asker
  that does not yet hold the responder's key, and a push cannot be
  sealed to a recipient whose key is unknown, so any such exchange
  would need a signed clear step this design does not add. Re-linking
  is done in the same sitting as the upgrade, by both operators: each
  removes the old peer (`peer rm`, whose waiting messages go with it
  and are counted as `messages_dropped` as today, to be re-originated
  if they still matter) and links again — by address once §6 beads 2
  to 4 have landed, or by the new `yardr-peer-2` invite (§3.4) where
  there is no address — pinning both keys at once as any first link
  does (§3.4). The remade link starts `ping`-only, as a new link does,
  and each operator re-applies what the old link carried (`peer allow`,
  `--expose`, `--trust`, `--reach`, intakes) for their own yard;
  nothing of the old link's grants survives the `rm`, and `peer show`
  on the remade link says it was made by address or invite, as it does
  for any new link. Today's links are the operator's own three yards,
  so this is one sitting's work, not a programme; a link made by
  address or by the new invite after the flag day never needs it,
  because both keys are pinned when it is made. A yard already
  connected to the edge also re-enrols once in that same sitting
  (`yardr edge connect <url>` again), so the edge's enrolment record
  gains the encryption key bead 2 (§6) publishes; until it does, that
  yard's record at the edge has no encryption key to publish, however
  findable it is set. Backups now hold two files; restoring without
  the encryption key is the same failure as restoring without
  `peer.key` today — a stranger to its peers — and `doctor` says
  which file is missing. Signing keys do not change; only the wire
  stops matching until both sides move and re-link.
- Pairing's three requests (`/peers/pair/hello|reveal|confirm`,
  [internal/pairing](../../internal/pairing/pairing.go)) are removed
  with the code transport they serve, in the bead that lands the link
  route (§6, bead 4): an old `peer pair` at a new yard finds no route
  (404), and says so as pairing already says a yard that does not pair.
  The edge learns to forward `/peers/link` beside `/peers/messages` in
  its own earlier bead (§6, bead 3); its pairing forwards stay until
  then and answer a removed route's 404 after.
  `internal/pairing` is deleted, not deprecated.
- The edge gains the WebFinger endpoint in its own bead (§6, bead 2)
  and stops reading peer bodies, which it could never verify anyway;
  its `/peers/<account>/<yard>` forwarding changes only as bead 3
  states (the link route added, peer paths forwarded unstripped so
  `@path` holds, §3.2). A
  yard's `hook_public_url` and account name do not change, and the
  address in §3.1 is the name a yard already has there; its edge
  enrolment gains the fields of §3.5 — the encryption key (`enc`) of
  the bullet above, presented by the yard from bead 1 and stored by
  the edge from bead 2, and the findable opt-in (`findable`),
  accepted and stored by the edge from bead 2 and presented by the
  yard once bead 4's setting exists — and a yard connected before
  then re-enrols once in the window to present the encryption key.

### 3.7. Who sees what

"Sees" means can read in the clear. Metadata first, because E2EE never
hides it: any carrier sees that two yards exchanged a request at a time,
of a size, between which endpoints. That is true in every shape, Matrix
included, and it is not counted as a failure of (b) below; what (b) removes
is the text.

| reader | today | this proposal |
|---|---|---|
| the receiving yard's operator | the message whole | the message whole (it is for them) |
| the edge | every body in the clear | endpoint names, timing, sizes; ciphertext only for bodies (not even the kind: it is inside the seal, §3.3); the `/peers/link` request body in the clear — the claimant's address, name, both public keys and endpoint, all of them public values the claimant publishes to be found (§3.3); and the pull query in the clear — `understands`, the `yardr` build, the roster it carries, the refused sequences and reasons, `after` and `wait` (`Caller.Pull` in [caller.go](../../internal/yards/caller.go) puts them in the URL query, and §3.2 signing `@query` authenticates them without encrypting them; a `GET` has no body to seal) |
| a proxy / tunnel / http path | every body in the clear | headers (method, path, signer key, intended receiver, time), that same `/peers/link` request body in the clear, and that same pull query in the clear, signed and not sealed; ciphertext only for bodies |
| the other yard's crew, beyond the roster | nothing (roster gates delivery) | nothing; and nothing more is published to be found |
| a stranger with the address | nothing to look up | endpoint and both public keys (they are public); and, by asking, one pending record with no grants until the operator approves (§3.4) |
| whoever later holds a recorded wire | every body | ciphertext; openable only with the receiver's encryption key (§3.3) |
| whoever holds `peer.key` (signing) | can forge as the yard | can forge as the yard; cannot open bodies |
| whoever holds `peer-enc.key` | — | can open bodies sealed to the yard; cannot forge |
| an internal or private service near a yard (loopback, the LAN, link-local, any non-global address) | nothing asks it anything | still nothing, on the stated assumption: neither a claimed address nor the endpoint its record names can make the yard dial a known private or special-use address (§3.4's allowlisted addresses, checked at storage and again at every dial, dialled literally, no proxy, no followed peer redirect). The check refuses the ranges it knows; it assumes the yard's network does not route a globally numbered address to an internal service, and a network that does keeps findable off (§3.4) |
| whoever controls a findable host (or its edge account) and names a hostile endpoint in its record | no record exists to name one | still nothing dialled: an `http`, IP-literal or privately resolving endpoint is refused at storage and keeps no pending record; one that rebinds later is refused at dial time (§3.4) |
| whoever controls the discovery host at first link (the edge serving a yard's record, or a self-hosted yard's own host) | no record exists to control | inside the trust boundary at that moment: it can publish the genuine signing key with an X25519 key of its own, and a yard that links on that record verifies the genuine signing identity but seals bodies to the controller, who can read them (§3.1). Past first link it controls nothing already pinned: a later record that disagrees is a changed key, refused and shown, and bodies stay sealed to the pinned key. The optional `peer verify` fingerprint (Q1, §5) does not catch this: at six digits (about 20 bits) a host that chooses the published encryption key can grind candidate keys to a matching display value, so it is an accidental-mismatch check only (below) |
| a public host an attacker names in a claimed address | nothing | one bounded, quota'd HTTPS GET per claim (5 s, 32 KiB, §3.4), learning the yard's public source address and that its address was claimed, at that time; no body, no credentials, no second URL is ever fetched from what it answers beyond a re-validated redirect |

Four honest limits. First, the trust boundary at first link, stated
plainly: the edge and any host that serves a yard's WebFinger record
are inside it at that moment. Version 1 trusts them — the edge is the
operator's own, and the pin is taken over that host's TLS (trust on
first use), exactly as the edge's own key is first trusted today.
End-to-end sealing therefore protects bodies against every carrier
*past* first link — the edge as forwarder, a proxy, plain http, a
recorded wire — and not against a discovery host that was already
malicious or compromised when the link was first made: such a host
can pair the genuine signing key with an encryption key of its own,
and the linking yard will seal to it while verifying a genuine
signer. No stronger first-link authentication is built in this
version (§4). The optional fingerprint over both keys (Q1, §5) is
not that check: six digits are about 20 bits, and a discovery host
that chooses the published X25519 key can grind roughly a million
candidate keys to the same display value, so `peer verify` catches
only an accidental mismatch — the two sides showing different
digits because they pinned different values by mistake or staleness
— and proves nothing against a host that set out to substitute a
key. Protection from a hostile first-link host stays left out
(§4). Second, the roster still names crew and users to the linked
peer — that is delivery working, not leakage, and it is not published in
the JRD; the JRD names the yard, never its people, so being findable is a
property of the yard alone and exposes nobody on it (yards only is
decided, Q2, §5). The pull query above is the third limit, stated
plainly. When a yard collects what waits for it, the edge reads in
the clear which peer it asks, what that yard understands, its
`yardr` build, the roster it carries for that peer, the sequences it
refused and the reasons, and `after` and `wait`. In short, the edge
sees which yards a yard is linked with, its version, and its refusal
reasons at collection time. That exposure is accepted for this
version (ask 13427, §5 Q6). The edge is the operator's own and TLS
covers the wire to it. No hiding is built in v1. Sealing that
metadata is left out (§4). And the endpoint in the JRD is the edge
peer URL, so the edge still learns that a lookup happened and, as
carrier, which yards talk. Hiding that is the multi-yard routing
question, out of scope here.

The link route's fetch deserves its own honesty. Before this design, a
stranger could make a yard do nothing but refuse them; now, with a
freshly minted key and a valid signature under it, they can make the
asked yard attempt one guarded HTTPS lookup of a public name they
choose, inside §3.4's quotas. That is the price of checking a name
before an operator is involved, and it is paid deliberately: the yard
cannot be aimed at its own network, cannot be made to follow a redirect
it has not checked again, cannot be handed a large or slow answer, and
cannot be made to repeat the fetch without bound — but the public host
behind the claimed name does learn that a yard looked it up, and a
yard's operator who never wanted a lookup at all still has the invite
line, which fetches nothing.

## 4. Left out

**Matrix as the transport (shape 1) is not proposed.** It was weighed as
the bead asks, and it answers (a) and (b) in the standard's own terms:
MXIDs with `.well-known` delegation are findable names, and Olm/Megolm
(vodozemac; `goolm` from mautrix-go) is real E2EE with a homeserver
storing and forwarding for a sleeping laptop. It is left out because of
what it replaces and what it costs, each checkable:

- *It rebuilds §2.5, it does not carry it.* Mail, asks and beads become
  custom event types in an invite-only room, but the kinds' two-direction
  allows, the roster's 32-name gate, trust and reach, intakes matched by
  git remote, the hour limits and every refusal reason have no room
  primitive; they become room state plus yardr checks over a synchronised
  event DAG, where today they are one store write. The single-writer
  store (AGENTS.md: "one store (bbolt) with one writer") gains a second
  synchronised state machine whose ordering is eventual, not local.
- *Someone must run a homeserver.* One per yard is a second server —
  database, media store, federation TLS, upgrades — beside the yard, for
  every operator who today runs one binary; Synapse is the reference
  implementation (its documentation recommends the monolith for small
  instances) and is sized accordingly, and the lighter servers trade
  that weight for maturity — Tuwunel, which presents itself as the
  successor to conduwuit, and Dendrite, whose repository states it is
  in maintenance mode. A shared
  one (the edge hosting it, or matrix.org) keeps that weight in one place
  and pays for it in the next two points instead. A shared homeserver
  also rate-limits its clients: the Matrix Client-Server API
  recommends rate limiting and defines the 429 answer for it, while
  the thresholds themselves are each homeserver's own, and a yard that
  syncs rooms for beads counts against them like any busy client.
- *The homeserver sees the metadata E2EE does not hide.* With (b) met for
  bodies, the homeserver still sees which rooms exist, who is in them,
  when events were sent and how large — the link graph of every yard on
  it, in one operator's logs. Today's edge sees that for the links it
  carries, but a yard can leave the edge; a Matrix yard cannot leave its
  homeserver without changing its name.
- *Verification doubles.* Matrix E2EE trusts devices, verified by SAS
  (emoji or decimal comparison) or cross-signing. A yard would be a user
  with devices — one per yard, or one per crew member, each a device to
  verify, back up (key backup, secret storage) and lose. yardr already
  has one verification ceremony, the six-digit check over the keys
  themselves; Matrix adds a second, over different keys, and the two
  must be kept from contradicting each other in the operator's head.
- *Group encryption is the wrong shape for a link.* Megolm is a group
  ratchet: efficient for rooms, with forward secrecy weaker than Olm's
  per-message ratchet and room keys shared over history. A yard link is
  two parties; paying group semantics (key rotation on membership,
  withheld keys, undecryptable-history handling) buys nothing a sealed
  body does not.

Nothing here says Matrix is a poor standard; it says the standard carries
a chat system's state, and yardr's link is not one. If a bridge is ever
wanted — a pack that mirrors mail into a Matrix room for a human who lives
there — it can be built over the sealed link without touching it, which
is the same place §1 leaves an ActivityPub bridge.

**ActivityPub / ForgeFed as the core** stays set aside, for the bead's
reasons: open-by-default federation, unencrypted direct messages, push
only (a sleeping laptop loses what a pull would have kept), and Forgejo's
own "experimental" for its federation. Naming it only as a later bridge
(pack) stands.

**`did:web` beside WebFinger** is left out (§3.1): same keys, more
machinery. If a future standard consumer needs a DID document, it can be
served from the same keys the JRD publishes, as a second view of one
record, without moving the lookup.

**The `acct:` address form** is left out as the primary form (§3.1).
The bead's example (`yardmaster@<someone>.yardr.dev`) reads naturally,
but it needs a host per account or a delegation rule to find its
lookup host, where an `https` URL address names that host itself and
needs no wildcard DNS or certificate. Nothing is lost that cannot be
added: the same JRD can later be served for an `acct:` resource as an
alias, once someone wants the shorter thing to type.

**A stronger authentication of discovered keys at first link** is
left out (§3.7). The missing step would be a challenge-bound check, a
key-transparency log, or any other way for a yard to distrust its own
discovery host when it first pins a link. Version 1 puts the edge and
the record's host inside the trust boundary at that moment. The edge
is the operator's own, pinned over its TLS. That is the trust the
edge's own key already gets today. The optional fingerprint over both
keys (Q1, §5) does not fill this gap. Six digits are about 20 bits.
A host that chooses the published X25519 key can grind candidate
keys to the same display value. The check catches only an accidental
mismatch. Designing a first link that survives a malicious discovery
host is a design of its own. It is not smuggled in here.

**Deriving the encryption key from the ed25519 key** is left out (§3.3):
possible on paper, unexposed by the standard library, and one trap
(clamping, cofactor) per future reader, to save one file.

**Rotating either key** is left out. Neither key rotates today
(`peer.key` is made once; §2.2), and this proposal adds no rotation for
the new one: a yard that must change its encryption key (lost file,
suspected copy) removes each peer and links again by address or invite,
which pins the new key exactly as a first link does; what was waiting
for the removed peer goes with it, as `peer rm` drops it today
(`messages_dropped`), and is re-originated if it still matters — the
same price §3.6 pays once for every pre-flag-day link. A signed
announcement that a peer should seal to a new key next time, with a
window that opens under both, is a design of its own (it is most of
Open Question 3's option (ii)) and is not smuggled in here as a
`PeerSays` field that quietly retargets seals.

**A mailbox or queue on the edge** stays refused, as it was in
yardr-hosted-tm9y: it would make the edge a second store and a second
trust root, holding ciphertext it cannot verify for yards that are away,
to solve a problem the sender's outbox already solves (§2.4).

**Resurrecting a given-up message by command** is left out (§3.6). A
`peer retry` that un-failed the outbox could not keep the word `failed`
already kept: a file's bytes are stripped as it is given up, so there
is nothing whole left to resend; a sent bead's stay has already ended
as `undelivered` and its flow has moved, so delivering it late would
have the peer work a bead this yard has given up on (its return would
arrive stale, as returns after a stay ends already do); and a mail's
writer has already been told it was not delivered. Re-originating from
the source — the flow's own `undelivered` path, a fresh mail or ask —
says the true thing at each step, and needs no un-telling machinery.

**A discovered endpoint on a private address, or over plain `http`,**
is left out (§3.4). Discovery is the internet-facing way in: its
endpoint must be an `https` name the world can resolve to global
addresses. Two yards on one LAN, or one reached through a tunnel the
operator runs, link by invite as today, where the operator types the
URL and the responsibility is visibly theirs; auto-following a record
to the same address would hand that choice to whoever answers DNS.

**Per-crew-member keys, rooms or addresses** are left out: the link's
unit today is the yard — one key, one roster, names gated by exposure —
and nothing in (a) asks a crew member to be findable before their yard
is. Crew findability is decided against for now (Q2, yards only,
§5), not an open question here, and not a second key system.

**Replacing the pull with push only, or the held question with a
socket**, is left out: the held pull is what lets an uncallable laptop be
a full peer (peers.md, "Push and pull"), and it survives every path in
§3.7 unchanged, capped holds included.

**Sealed pull metadata** is left out (§3.7). Sealing the bodies does
not seal the pull's question. `understands`, the `yardr` build, the
roster, the refused sequences and reasons, `after` and `wait` ride in
the `GET` query in the clear. The signature covers `@query`. The edge
and any carrier can read them. In short, the edge sees which yards a
yard is linked with, its version, and its refusal reasons when it
collects mail. That exposure is accepted for v1 (Q6, §5). Moving the
values into a sealed body, or encrypting the query itself, would
rebuild the held pull around a body it does not have today. It is
not paid for here. The edge is the operator's own and TLS covers the
wire to it. What the carrier learns is named in §3.7's table. The
design does not hide it by calling the link encrypted whole.

## 5. Open questions

Q1 to Q6 were to decide at `decide` and are decided, recorded below
as they were settled on 26 Oct (asks 13316 to 13319, 13366 and
13427). The proposal above already reads as they decide. None of
them is decided anywhere else in this document except as recorded
here. No question in this section remains open.

**Q1. What pins a discovered key: TLS at lookup, or the six-digit check
beside it?** §3.1 pins what the lookup returned, and §3.4's reverse
 lookup trusts the same root in the other direction. Options: (i) the pin
 alone — linking is one command and an approval, and a wrong key means
 the address host's TLS or the edge served it, which is also how the edge's own
 key is first trusted (edge.md, "The first step is trust");
 (ii) the pin plus an optional `peer verify`, for operators who will
 compare it;
 (iii) the check required on every new link, as today — discovery then
 moves the key but not the ceremony, and (a) is only half answered.
 Cost: (i) trusts the host once; (ii) keeps a comparison available
 without taxing every link; (iii) keeps two operators in contact, which
 is what (a) asked to remove. *I would take (ii).* **Decided (26
 Oct, ask 13316): (ii).** The address URL's host TLS pins the key.
 An optional `peer verify` shows six digits for those who will
 compare them. What those digits are is settled here. Optionality
 alone does not say what comparing them proves. `peer verify` is a
 static fingerprint. It is six digits over both yards' pinned
 signing keys, both yards' pinned encryption keys and both
 addresses. The two sides are sorted so each yard computes the same
 value from its own stored link alone. The check is an
 accidental-mismatch check under the trusted-host model and nothing
 more. If the two sides show different digits, they pinned different
 values by mistake or staleness and the operator should not trust
 the link until that is explained. If they show the same digits,
 that alone proves little against a hostile host. Six digits are
 about 20 bits. A host that chooses the published X25519 key can
 grind roughly a million candidate keys to the same display value.
 The fingerprint therefore does not catch an active substitution at
 first link and does not prove both yards pinned the same keys
 against such a host. Protection from a hostile first-link host
 stays left out (§4). The check is not today's pairing check and
 must not be read as it. Pairing's six digits are a short
 authentication string over a commitment and fresh nonces from both
 yards (`Transcript.Check` in
 [internal/pairing/pairing.go](../../internal/pairing/pairing.go),
 over the commit-then-reveal transcript). They bind a live exchange
 neither side could have precomputed. The fingerprint binds no live
 exchange. It has no fresh nonces and no commitment. It proves
 nothing about who is at the other end now. No challenge protocol
 is built in this version.

**Q2. Whose name is an address: yards only, or crew and users through
 the yard?** The bead's example is `yardmaster@<someone>.yardr.dev` — a
 crew member's name at the account's domain. This is a scope decision,
 not a later nicety: option (i) answers (a) for yards and leaves crew
 findability undesigned until it is asked for. Options: (i) yards only
 (an edge yard at `https://my.yardr.dev/<account>/<yard>`, §3.1); the roster then says who of the yard may
 be written to, as today, and nobody is more findable than their yard;
 (ii) also crew and users, each at an `https` URL under the yard's own
 host, listed only for names the operator chooses to publish,
 separately from the roster (being findable is a bigger grant than
 being writable-to by one linked peer). Cost: (ii) publishes people to
 strangers, which the roster
 was built not to do; (i) makes a stranger ask the yard, not the person.
 *I would take (i) now, and let (ii) be asked for.* **Decided (26
 Oct, ask 13317): (i), yards only.** The follow-up point is folded
 into §3.1. The address is the `https` URL itself. The `acct:` form
 is left out as a possible later alias (§4).

**Q3. Is HPKE base mode enough, or is forward secrecy owed now?** §3.3
 names the gap: a later-taken encryption key opens recorded ciphertext.
 Options: (i) base mode — one static key, no prekey service, the `age`
 bargain, stated in the docs as plainly as §3.3 states it;
 (ii) signed ephemeral prekeys (an X3DH-shaped step) published in the
 JRD and rotated by the yard — real forward secrecy for bodies at rest
 on a recorded wire, at the cost of prekey storage, exhaustion and
 one more thing for `PeerSays` to carry and get wrong;
 (iii) a full ratchet — a messaging protocol's machinery for a link that
 sends letters, not a conversation. Cost: the recorded-wire reader is
 real but must first record and later take a key; (ii) is a design of
 its own, not a flag. *I would take (i), with the envelope's room for a
 prekey field kept, and revisit when a yard's bodies are worth more
 than its key.* **Decided (26 Oct, ask 13318): (i).** Simple sealing
 now. The gap is stated in the docs. The envelope keeps room for a
 prekey field.

**Q4. Does the edge serve WebFinger for every yard on it, or only yards
 that opt in to being findable?** Options: (i) every connected or hosted
 yard is answerable at its address, since the address is the account and
 yard names the edge already routes by; (ii) `yardr edge findable`
 (a setting, off until set) — a yard that never asked to be found is not
 in the directory, and linking by name works only where both yards chose
 it. Cost: (i) makes every trial yard enumerable by name-guessing over
 accounts; (ii) makes (a) a feature a yard turns on, and some links fall
 back to invite lines. *I would take (ii)* — findable is a publishing
 act, and publishing is the operator's, like exposing a name is.
 **Decided (26 Oct, ask 13319): (ii), opt in.** The edge serves the
 lookup only for yards whose operator turned findable on.

**Q5. How is the flag day crossed: in one announced window, or with a
recovery for links that miss it?** That there is a flag day, with no
 release that reads both signature schemes, is settled by the 11 Oct
 decision and is not asked again here (§3.6). What is not true, and
 this question no longer offers, is that yards may upgrade in any
 order over any gap and queued messages simply resume: past the
 give-up horizon (§3.6) they are failed for good. Options: (i) one
 announced window for the yards linked today — the trial's handful —
 shorter than that horizon (24 tries, most of a day; less for a
 bead's own tries), in which every operator upgrades, so each link's
 pause is an hour nobody messages through. A window shorter than the
 horizon does not by itself save every queued push: a message already
 near its final try when the window opens, or one under a lower bead
 limit, can still run out inside the window, and is then failed and
 re-originated exactly as a message that missed the window is — the
 window bounds the usual pause, it does not promise no loss; a yard that misses
 the window is upgraded when found, and what it was given up on is
 re-originated (a new mail, the bead's `undelivered` outcome, a fresh
 ask), as §3.6 states; (ii) no window, plus a recovery command that
 retries a link's given-up messages once it answers on the new wire.
 Cost: (i) asks for coordination once, for a known handful of yards,
 and keeps `failed` meaning failed; (ii) sounds kinder and cannot
 deliver it — a given-up file's bytes are already stripped, a sent
 bead's stay has already ended `undelivered`, and a mail's writer has
 already been told, so the command would resend some messages late
 and others never, under a name that promises all (§4). The next yard
 to upgrade late under (i) pauses and may lose what was pushed to it
 meanwhile, exactly as a yard asleep past the horizon does today;
 pull-waiting messages lose nothing under either. *I would take (i),
 and announce the release as breaking either way.* **Decided (26
 Oct, ask 13366): (i).** One announced upgrade window. Connected
 yards upgrade together in that window and re-link and re-enrol
 then (§3.6). There is no recovery command for a yard that upgrades
 later. It re-links and re-enrols when it does. What was given up
 on meanwhile is re-originated. The window carries the edge cutover
 with it. Bead 3's edge is deployed and bead 1 merged inside it, in
 the order §3.6 and §6 state. Neither is crossed alone.

**Q6. May the edge see pull metadata in the clear?** §3.7 names what
the pull query carries. When a yard collects what waits for it, the
edge reads which peer it asks, what that yard understands, its
`yardr` build, the roster it carries for that peer, the sequences
it refused and the reasons, and `after` and `wait`. In short, the
edge sees which yards a yard is linked with, its version, and its
refusal reasons at collection time. Options: (i) accept that
exposure for v1 because the edge is the operator's own and TLS
covers the wire to it, and state it plainly in the threat model;
(ii) seal or move that metadata, which rebuilds the held pull
around a body it does not have today. Cost: (i) leaves link
metadata with the carrier by design; (ii) pays a rebuild to hide
metadata the operator already trusts the edge to carry past. *I
would take (i).* **Decided (26 Oct, ask 13427): (i), accept.** The
edge may see that metadata when a yard collects mail. No hiding is
built in v1. §3.7 states it plainly and §4 leaves sealed pull
metadata out.

## 6. The beads it would become

Numbered in the order they are proposed; the order they land and are
deployed in is stated with each bead, because the flag day is a
coordinated deployment and not a landing alone (§3.6, Q5 in §5):
bead 2 lands first, bead 3 merges next and waits undeployed, bead 1
waits at `approved` and merges inside the announced window (when
bead 3's edge is deployed), and beads 4 and 5 follow. Each names where to look and what "done"
means; each is sized for one builder without a further design, and each
touches one depot only — the two edge beads are yardr-hosted's, the rest
yardr's — so no builder lands half a change in a repository they are
not working. The wire
change is one bead because it must be: sealing and re-signing both break
peer communication with the old build and both edit the same caller,
receiver and key path, so landing either alone would strand every link
between two half-new wires for no benefit. After that flag day the rest
is additive, each bead on the ones it names.

**Bead 1 — The flag day: RFC 9421 signatures and HPKE-sealed bodies
(yardr).** Goal: in one change, the §3.2 profile replaces the
`Yardr-Peer-*` triplet (covered `Yardr-Peer-To` and
`Yardr-Peer-Nonce`, `keyid` lookup, `created`/`expires`,
`@path`/`@query` derived from the URL actually sent to, answers
bound by every request component with `;req` — `yardr-peer-to` and
`yardr-peer-nonce` included — plus the echoed `Yardr-Peer-Nonce`
under cover as the answer's own component, with `@signature-params`
never a covered component) and
every peer body — push payload, pull page, signed answer — crosses as
the §3.3 envelope (`info` the protocol alone, sender and receiver
signing keys as additional data, kind inside the seal, sealed and
opened through `hpke.NewSender`/`Sender.Seal(aad, …)` and
`hpke.NewRecipient`/`Recipient.Open(aad, …)`), opened before
any §2.5 check. Each yard gains `peer-enc.key`; `yardr peer key`
prints both public keys; `store.Peer` gains the
peer's encryption key; `PeerSays` does not gain it, and no other
exchange teaches it (§3.3, §3.6); the invite line's next version
carries both keys; and enrolment presents the yard's encryption
public key beside its signing key, in the enrolment body itself, so
a yard that enrols on this build presents the key the edge bead (§6,
bead 2) stores and publishes. A stored peer with no encryption key
is refused for sealed traffic with the re-link line of §3.6, before
any dial. The wire caps move with the seal and the plaintext
caps do not (§3.3): the receiver's and caller's read limits are derived
from the envelope encoding of `MaxPeerFileBody` (keeping the 4 KiB and
1 MiB slacks they carry today), but the receiver grants the file-sized
limit only after the header signature has held — it verifies from
headers alone first (`keyid`, `Yardr-Peer-To`, `created`/`expires`,
the covered components and the `Content-Digest` header value, §3.2),
then reads the body and checks it against the signed digest — and an
opened body is still refused over its plaintext bound. A request that
fails that header check reads nothing past `maxRequest`. This bead
removes yardr-zd25's interim bound on the new wire: that bead's
two-step read (up to `maxRequest` first, a body past it taking one of
four large-read slots, 503 `too_many_large` when none is free) bounds
today's protocol, where the signature covers the body and so cannot
be checked first, and it stays in place until this bead lands; once
the header signature gates the large limit, a stranger cannot occupy
a slot because they never earn it, so the four-slot semaphore goes
with the old `verify` and is not carried onto the new wire. Where to look:
[internal/yardkey](../../internal/yardkey/yardkey.go) (`SignRequest` /
`VerifyRequest`, their response pair, `SealBody` / `OpenBody` over
`crypto/hpke`'s `NewSender` / `Sender.Seal(aad, …)` and
`NewRecipient` / `Recipient.Open` — not the top-level `hpke.Seal`,
which takes no additional data), [internal/yards/caller.go](../../internal/yards/caller.go)
and [receiver.go](../../internal/yards/receiver.go) (sign, seal, verify,
open, in that order; the caller derives `@path`/`@query` from the URL
it sends to and the receiver from the target it receives, routing on
its ending), [internal/store/peers.go](../../internal/store/peers.go)
(the stored encryption key, and the refusal of a record that has
none; `PeerSays` is left as it is),
[internal/edgelink](../../internal/edgelink/edgelink.go) (the
encryption public key added to the enrolment body `Enrol` sends —
`{"key": …, "name": …}` today — beside the signing key),
and
docs/peers.md ("The requests", "What a signature does" gains "and what
a seal hides"). Done when: requests verify against a published RFC 9421
test vector and by one non-yardr library in the test; the isolated-yard
trial of peers.md ("Trying it on one machine") passes with bodies on
the wire verifiably ciphertext (a test asserts the mail text and the
kind are absent from the request bytes); a request signed for a
different peer (`Yardr-Peer-To` not the receiver), a wrong-key or
tampered envelope, and a request signed the old way are each refused as
§3.2 and §3.6 say (the last as a plain unsigned 401, never a kind
refusal); a request that carries a known peer's `keyid`, a bad header
signature and a body over `maxRequest` is refused 401 with no more
than `maxRequest` read (the receiver verifies the headers alone
first and grants the file-sized limit only after that holds, §3.2,
§3.3), and the same holds for an unknown `keyid` and for a
`Yardr-Peer-To` that names another yard; a file at the yard's own maximum (a 32 MiB file where the
setting allows it), byte-identical by hash after crossing, is taken
both as a push and inside a pulled page, its wire body larger than
today's caps and within the derived sealed bound, while a file one
byte over the plaintext setting sealed inside a lawful wire body is
refused `too_large` and a wire body over the sealed bound is refused
413 before opening; a peer record stored before this bead (no
encryption key) is refused with the §3.6 re-link line — `peer ping`
fails with it, `peer show` and `doctor` name it, and the dispatcher
makes no dial and counts no try to it — and after that peer is
removed and added again with the other's new invite, a ping crosses
sealed both ways; across the flag day, on a link that already pins
an encryption key (made by the new invite in the test), a short gap
and a long one are each proven: with the second yard upgraded while
a queued message still has tries left, that message is delivered
with no re-link, and with the second yard held on the old build (or
answering only unsigned 401s) until the queued message's tries run
out — the test sets the try count low rather than waiting out 24 —
the message is marked failed, counted by `peer show`, and still not
delivered after that yard upgrades, while a message that only
waited to be pulled crosses on the first pull after the upgrade
however long the gap was; enrolment presents the yard's encryption
public key beside its signing key in the enrolment body; a request arriving at the
receiver's own address-URL path (the full edge path, unstripped)
verifies with `@path` as sent, as a direct one does at
`/peers/messages`; an answer covers every request component with
`;req` — `@method`, `@authority`, `@path`, `@query` and
`content-digest` where present, and `yardr-peer-to` and
`yardr-peer-nonce` — beside its own echoed `yardr-peer-nonce`, and an
answer replayed for a second request whose fresh `Yardr-Peer-Nonce`
differs from the nonce the answer echoes (or whose method,
authority, path, query, body digest or intended peer differs) does
not verify for that request, on the messages route and on the
`linked` answer of §3.4 alike, and no covered component in either
profile is `@signature-params`; and `sh .yardr/test` passes. This bead *is* the flag
day of §3.6 and Q5 (decided, §5); no other bead may land between its
halves, because there are no halves. Depends on beads 2 and 3 for
merging, not for building: it builds and is tested alone, but it
waits at `approved` until the announced window, and merges only
inside it, when bead 3's edge is deployed (§3.6) — a yardr landing
upgrades the yard it lands on, and a yard on this wire calling
through a still-stripping edge cannot verify. Bead 2's enrolment
record must already be live for the re-enrolment in that window,
and bead 3 must be merged and ready to deploy.

**Bead 2 — WebFinger at the edge (yardr-hosted).** Goal: the edge
answers the §3.1 lookup for its yards from the enrolment record it
holds — account and yard names, the yard's signing key as enrolled,
the edge peer URL as endpoint, the yard's encryption key (`enc`),
and its findable opt-in (`findable`, §3.5) — and serves no yard Q4
keeps unfindable. This bead is the edge side of that record: it
extends the enrolment schema to accept and keep both new values a
yard presents — the encryption key (in production, since bead 1)
and the findable bit beside it (in production, once bead 4's setting
presents it) — and publishes only what was enrolled, nothing it
invents or fetches. Where to look: the
router beside its `/peers/<handle>/<yard>` forwarding
(`router/internal/router/router.go`, beads yardr-hosted-tm9y and
yardr-txwg), the enrolment handler that yard's signing key already
arrives on (`router/internal/router/edge.go` — its enrolment body
accepts only `key` and `name` today), and the map row that keeps it
(`router/internal/edgemap/map.go` — it stores only the yard's signing
key today). Done when: enrolment with an encryption key and a
findable bit stores both on the map row, enrolment without either
(a yard from before beads 1 and 4) still stores as today (no
encryption key, not findable), a lookup for an enrolled yard whose
`findable` is true returns the §3.1 JRD with endpoint and both keys
exactly as enrolled (and, for a yard enrolled without an encryption
key, no encryption-key link rather than a wrong one), an unknown
yard is 404, an enrolled yard whose `findable` is false or absent
is 404 on WebFinger (tested, not merely unlisted), the lookup is
answered by the edge without dialling the yard, and the router's
tests pass. No dependency, and it lands first: the encryption key
and the findable bit are values its tests enrol with directly, so
this bead finishes on the schema and the map row alone, before any
yard presents either in production. The yardr side of that presentation is bead 1, which
merges later, inside the window; a yard already connected re-enrols
once in that window (§3.6) to present it, against the record this
bead has already made live.

**Bead 3 — Forward `/peers/link` at the edge, unstripped
(yardr-hosted).** Goal:
the edge forwards `POST /peers/link` to a yard so a link ask can reach
an edge yard at all, and — for the RFC 9421 profile of §3.2 to hold
through it — on every peer route it forwards the path unstripped:
the request target the yard receives is the one the sender signed
(`/peers/<handle>/<yard>/peers/messages` or `…/peers/link`, query
included), not a stripped peer-local remainder, and the yard's answer
is returned as before. Today the router forwards only `/peers/messages`
and the three pairing routes (`peerAllowed` in
`router/internal/router/router.go`; router README's `/peers/*` row),
strips the `/peers/<handle>/<yard>` prefix on the way (the rewrite
`internal/yards/edge_test.go` in yardr stands in for), and anything
else under the prefix is 404 at the edge. Where to look:
that allowlist, the dialled-forwarding path beside it, and the
router's rewrite tests, which change with the rewrite. Done when:
a signed link ask through the edge reaches the yard with its full
signed path, a signed message does likewise and its signature
verifies at the yard against `@path` as sent (not against a stripped
path), an unlisted peer path is still 404 at the edge,
the pairing forwards are untouched, and the router's tests pass.
Depends on bead 2: both beads edit
`router/internal/router/router.go` in yardr-hosted, so they land in
that order rather than in parallel in one file. It merges after
bead 2 and is then held undeployed — a landing never redeploys the
live router — until the announced window (§3.6, Q5 in §5), when it
is deployed together with bead 1's merge; deployed alone in front of
yards on the old wire it would hand the old receiver a request
target its signature never covered, exactly as bead 1 alone behind
a stripping edge cannot verify. Bead 1 depends on it for merging
(above), and it must be deployed before bead 4 can ask through it
and before bead 5 can sign and verify through the edge.

**Bead 4 — Link by address (yardr).** Goal: `yardr peer link
<https-url>` (§3.4) looks the address up, pins what it found, stores
the peer ping-only and awaiting approval on the asking side, and asks
over the new `POST /peers/link` route; the asked yard verifies that
route in its own order (recipient, possession under the claimed key,
then a guarded reverse lookup matching both keys and the endpoint,
§3.4), keeps verified asks as durable pending links (7 days, at most
32, one per signing key), and its operator answers with `yardr peer
requests`, `yardr peer approve` and `yardr peer decline` in a terminal —
never a peer message, never a run ask. The asking side sends the gated
peer nothing — no push, no pull, no counted try — until a checked
`linked` or a signed message heard from it clears the gate. This bead
also carries the yardr half of being found that bead 2 does not: the
findable setting itself (`findable` at enrolment, which bead 2
accepts and stores, §3.5), decided opt-in in Q4 (the encryption key it
publishes is already presented since bead 1), `yardr peer verify <peer>`
shows the static fingerprint of Q1. It is six digits over both
yards' pinned signing keys, both pinned encryption keys and both
addresses. The two sides are sorted so each yard computes it from
its stored link alone. The command is an accidental-mismatch check
only, under the trusted-host model of Q1. Different digits mean
the two sides pinned different values. Same digits do not prove
both pinned the same keys against a hostile host. Six digits are
about 20 bits and a host that chooses the published key can grind
to a match. It is not pairing's nonce-bound check. The command is
for operators who compare it, and a yard serving
its own host
answers the same JRD from its own keys. Pairing's code transport and
[internal/pairing](../../internal/pairing/pairing.go) are removed in
this bead, once the route stands. Where to look:
[internal/yards/receiver.go](../../internal/yards/receiver.go) (the
route beside `Handler`'s messages, dispatched before any `PeerByKey`
lookup, and the guarded WebFinger client §3.4 specifies),
[internal/edgelink](../../internal/edgelink/edgelink.go) (the
findable setting presented at enrolment; the encryption key beside it
is bead 1's),
[internal/yards/caller.go](../../internal/yards/caller.go) (the guarded
dial for lookup-stored URLs: re-resolve, allowlist, dial the validated
address, no redirects),
[internal/dispatch/peers.go](../../internal/dispatch/peers.go) (the
peer pass skipping an awaiting-approval peer whole), a `PairStore`-shaped store surface
for pending links in [internal/store](../../internal/store/peers.go),
[internal/cli/peers.go](../../internal/cli/peers.go) (`link`,
`requests`, `approve`, `decline`, `verify`, `show`, `doctor`'s peers line), the
operator-only gates of peers.md "Who may do what" and
`internal/store/runask_alone.go` (which must keep refusing all of it as
a card), and docs/peers.md ("Linking two yards" rewritten around the
lookup and the pending ask). Done when: two isolated yards link by
address alone (no code, no invite line) and the §3.4 transcript is real
output; a request whose claimed keys disagree with its own address's
record, an already-linked key, and a 33rd pending ask are each refused
and counted as §3.4 says; a link request signed for another yard, one
whose signature does not hold under its claimed key, and one whose
record matches on the signing key but not the encryption key or
endpoint are each refused under §3.4's bootstrap order with no pending
record kept, while the messages route still refuses the same key as
an unknown peer; a claimed address whose host resolves to a private,
loopback, carrier-grade-NAT (100.64.0.0/10), documentation or
IPv4-translation (64:ff9b::/96, 64:ff9b:1::/48) address
is refused before any dial, an `http` redirect is
not followed, an answer over 32 KiB is not kept, and
lookups past a §3.4 quota fetch nothing; two yards at one host (two
resource URLs under one account) are never served each other's record
from the lookup cache, which is keyed by the full normalised resource
URL; `peer verify` shows the same six digits at both yards from their
stored links alone when they pinned the same values, and a link
whose other side pinned a different signing key or a different
encryption key by mistake shows different digits, with the help
text stating the check is accidental-mismatch only and about 20
bits; the endpoint cases stand
beside them: a record naming an `http` endpoint, an IP-literal host,
or a host resolving to any non-global address is refused at storage
in both directions (no peer stored, no pending record kept, the
reason shown), an endpoint that rebinds to a non-global address
between link and dial is never dialled (the guarded dial counts a
failed try), a peer endpoint answering a redirect to another host —
private or public — is not followed to it, and a yard added by
invite with an operator-typed URL, a LAN `http` one included, is
still called at that URL as today; approval creates only the ping-only link and
decline only the day's tombstone; a changed key at re-lookup is refused
and shown; a message queued to the gated peer before approval makes
zero `/peers/messages` attempts and keeps its try count at zero across
repeated dispatcher passes, and after approval — learned by a checked
`linked` or by the approver's first signed push — the same queued
message is delivered once with no requeue and no operator resend; a
`linked` answer recorded from an earlier check, replayed against a
new check whose fresh `Yardr-Peer-Nonce` the answer does not echo,
does not clear the gate and sends nothing;
and the gate passes. Depends on beads 1, 2 and 3 (it pins the
encryption key bead 1 makes, signs with bead 1's profile, is found at
the record bead 2 serves, and asks through the forwarding bead 3
adds).

**Bead 5 — The trial, then the docs whole (yardr).** Goal: the end-to-end
proof and the paper brought level, last because it judges the rest.
Where to look: contrib/isolated-yard (two yards, one reached only
through an edge stand-in), docs/peers.md, docs/edge.md ("What the edge
reads" rewritten for ciphertext), and the troubleshooting table's TLS
and 401 rows. Done when: the isolated trial sends mail, an ask and a
bead each way over the edge URL with bodies sealed and signatures
verifying through the edge — `@path` derived by the sender from the
full edge URL it sent to and by the receiver from the unstripped
target it received, the two proven equal there, for messages and for
a link ask alike — `yardr peer ping`
through a plain-http relay succeeds, the docs' claims match the
transcript, and the gate passes. Depends on beads 3 and 4 (the
unstripped forwarding, then the link it carries).

Not a bead here: a Matrix or ActivityPub bridge, a prekey service
(Q3, decided against for now), key rotation (§4), crew-level
addresses (Q2, decided yards-only) and an `acct:` alias (§4) each wait
on their own design, and multi-yard routing stays out of scope
as the bead states.






