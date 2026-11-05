# End-to-end encryption between a paired device and a connected yard

Design yardr-pqw3, proposed 2026-11-05. A proposal as it was decided on,
not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots.
The edge is the service at `https://my.yardr.dev` that stands in front of
yards: a yard dials out to it and keeps one WebSocket open to it (the
link), and webhooks, peer calls and device calls come down that link. A
device is one program of a user (footplate on a desktop, footplate-ios,
footplate-android) that calls a yard from outside it, let in by a token
the yard gave it at pairing. A connected yard is a yard a person runs on
their own machine and connects to the edge; a hosted yard (Station) is
one the service runs on its own box. End-to-end encryption (E2E) here
means the device and the yard hold keys the edge never learns, so the
edge carries ciphertext it cannot read, forge or replay. The yard's own
words are in `docs/glossary.md`. Every claim in section 2 names the file
it rests on; outside sources are linked and dated in the last section,
and were read on 5 November 2026.

## 1. The question

mckean, in mail 146448 and 146453 of 5 November 2026: "let's look into
that! would it make the experience worse? could it be optional?" — that
is, can a paired device and a connected yard encrypt their calls end to
end, so the edge at my.yardr.dev carries them without being able to read
them; what would a person notice; and could it be a choice rather than
a condition. This document answers with candidates for each of the
bead's seven questions, what each costs, where it fails and what it
assumes, and the ranking last in each — and, for the seven questions
mckean has since picked among (section 5), the pick. Nothing here
is built,
and `docs/edge.md` and the edge's public notice are not changed by it:
they are rewritten only after a pick, by the bead that builds it.

## 2. What exists today

### 2.1. The edge reads every device call, and the yard-key signatures hide nothing

TLS ends at Cloudflare and at the router (the yardr-hosted program that
is the edge's origin). `docs/edge.md`, "What the edge reads, and what it
never holds", states the consequence plainly: the edge reads every
webhook payload and its headers, and a device's calls pass it in the
clear too; the yard-key signatures prove who sent a frame and hide
nothing; nothing through the edge is end-to-end encrypted. What the
edge never holds is also stated there: repositories, hook signing
secrets, the yard's private key, and a connected yard's model
credentials.

The link itself is in `internal/edgelink/link.go` (the yard's side) and
in the router README's "The link" section (the edge's side). A yard
opens `GET /edge/link` with a signed upgrade (`SignRequest` in
`internal/yardkey/yardkey.go`), the edge answers a challenge, and each
end signs both 32-byte nonces (`SignLink`, the label `yardr edge link
v1`); the two nonces are the link's session. Every request the edge
sends down is then a signed frame (`SignFrame`, the label `yardr edge
request v1`) over the session, a sequence number, the request id, the
method and path, a digest of the headers (`HeaderDigest`), the body's
SHA-256, when the edge received it, and whether it waited in the queue.
The yard verifies each frame against the edge key it pinned at
`yardr edge connect` — trust on first use over HTTPS, the printed key
comparable with the one published outside the edge's own hosting
(`docs/edge.md`, "Connecting a yard"). The yard's answers on the link
are not frame-signed: the link's possession, proven at the hello, is
their warrant. All of this authenticates; none of it conceals. The
WebSocket runs over TLS that ends at the edge, so the router process
holds every frame's method, path, headers and body in the clear.

### 2.2. A device's call through the edge, frame by frame

`docs/edge.md`, "footplate through the link", and the router README's
"Device calls and the events stream" agree on the shape. The device
sends each call to the edge's one origin with three credentials and
names: the device token in `Authorization: Bearer`, its edge (Kratos)
session in `X-Edge-Token`, and which of the account's yards it means in
`X-Edge-Yard`. The router checks the session, strips `X-Edge-Token`,
`X-Edge-Presenter`, `X-Edge-JWT` and `Cookie`, keeps `Authorization`,
and sends the call down the yard's link as a request frame like a
webhook delivery's: method, path, headers, base64 body. The README says
of that header, in so many words, that the edge passes the device token
on and can read it. With the token, the edge could act as that device
within what the device may do, until `yardr user device rm` revokes it.

The bounds a channel must fit inside are also already fixed. A call
waits up to 30 seconds for the yard's answer (`EDGE_CALL_WAIT`); a link
holds at most 64 of a device's calls and streams at once; a call's body
is read to at most 1 MiB; the edge reads a link frame to 1 MiB of JSON,
about 700 KiB of body; a larger answer crosses as a stream — a head
frame, then `data` pieces, then `end` — to the edge's 16 MiB bound
(`maxDeviceAnswer`), with at most 64 MiB queued across a link's streams.
Requests are never streamed: a `PutBlob` over the body limit is not
carried. `GET /user/events` is carried as the stream it is, and while
nothing is written for 30 seconds the router writes an empty line to
the device, which is no event and which a client skips; the yard's side
of that stream is silent meanwhile. Keep-alive on the link is the
edge's and the yard's own `ping`/`pong` every 30 seconds, closed after
75 seconds of silence. None of these is a device-to-yard byte today.

### 2.3. Pairing gives the edge the code and the token on the first day

An operator opens a pairing with `yardr user device pair <user>
<device>` (`docs/users.md`, "Enrolled by a code"; `DevicePairOpen`,
`DevicePairRedeemBy` and `redeemDevicePairing` in
`internal/store/devicepair.go`). The yard shows an eight-character code
(about 40 bits), good for ten minutes and one device; five wrong codes
end the pairing; the code stands in the server's memory and nowhere
else. The device calls `POST <address>/user/pair` with the code and is
answered with its token, once, in the clear of that connection. Through
the edge that redeem call crosses the edge, so the edge sees the code
while it is good and the token as it is issued. One narrowing already
exists: a redemption the edge forwards carries `X-Edge-Presenter`, the
signed-in account the edge resolved, and the yard redeems the code for
the pairing's user alone (`DevicePairRedeemBy`); any other presenter is
refused as a wrong code. The store keeps only the token's SHA-256
(`tokenHash` and the `device_tokens` bucket in
`internal/store/devices.go`), so the yard at rest holds no token; the
edge in motion sees every one.

The yard already has the stronger pattern one door down, for yards.
Peer pairing (`docs/peers.md`, "Pairing" and "What pairing rests on")
runs a commit-and-reveal exchange keyed by an HMAC from the code
(PBKDF2, 600,000 rounds), and its safety is not the code but a
six-digit check over everything that crossed, which the two operators
compare: someone in the middle who gives each yard a key of their own
gets the two checks to agree by chance alone, once in a million. That
page says explicitly that the exchange is not a PAKE (SPAKE2, CPace),
with which the code alone would hold against someone in the middle,
and that `--yes` gives the check up. A device pairing has no check at
all: `docs/users.md` says there is none, because a device has no key —
what it is given is a spendable token, which whoever reads the connection
reads on every call after this one.

### 2.4. The one device answer the edge reads on purpose: the push probe

The push relay is part of the router (router README, "Push: the
relay"). A phone registers with `POST /user/push/register`, as it sends
every device call — device token, edge session, `X-Edge-Yard` — with a
body naming only its platform (`apns` or `fcm`) and its push token. The
router answers that call itself and forwards no part of its body
anywhere, but to learn whose device this is without trusting the
token's say-so it makes a `GET /user/me` probe to the yard with the
same `Authorization`, carried as that yard's `/user/*` calls are
carried, and the answer — the device's user, its device name and the
yard — is the one device answer the edge reads deliberately. Push
intents then ride the yard's link naming only the user, the device, the
kind (`mail` or `decision`) and a random stable event id, and what
leaves the edge for Apple or Google is a content-free wake: the kind, a
random wake id, a combined count and a link holding only that id. The
push token itself lives only in the relay's store and in the provider
request that spends it; no yard ever sees one.

### 2.5. Approvals and one secret already cross, readable; two things never cross

`docs/edge.md` and `docs/users.md` ("Approvals cross the edge") record
that `ApproveAsk` and `ApproveSetup` are taken from a paired device of
the operator down the link, and that the edge therefore sees the
device's token on every call, so a dishonest or compromised edge can
approve exactly the command stored on any ask, a strong (`trust` or
`code`) card included; what holds it is that the command is fixed on
the ask, that the operator paired the device, and that removal ends it
at the next request. One secret crosses on every connected yard: the
code a harness's sign-in page shows, given with `GiveSecret` so a phone
with no terminal behind it can sign the assistant in; the edge reads
the code with the token. Every other secret — an API key, the account a
setup approval types — is refused down the link on a connected yard and
answered at the yard's own machine (`inTheClear` in
`internal/rpc/user.go`, with `operatorCall` beside it); on a hosted
yard every secret crosses, because the service runs both the edge and
the yard's machine and the operator has no shell there. A session's
screen (`SessionScreen`) and the teammates list cross to the operator's
device on the same terms. Two things never cross the device link at
all: the grant list (`SourceGrants`) and the telemetry switch
(`TelemetryShow`, `TelemetrySet`). The gate is a policy in the yard,
not a property of the transport: the yard can tell a call came down
the link (its remote address is the link's), and refuses by name. E2E
changes what the edge can read of a call, not which calls the yard
takes from a device; any widening is a separate decision (section 3.3).

### 2.6. The web view, previews, peers and webhooks through the edge

The web view of a connected yard opens in a browser at the edge's
origin; the edge checks the account session, mints a one-minute JWT
(`X-Edge-JWT`), and carries the browser's requests down the yard's
second, `web`-role link to the yard's view on loopback (`docs/edge.md`,
"The web view of a connected yard"). The view's pages and scripts are
the yard's bytes, but they are delivered over the edge's origin under
TLS that ends at the edge: a browser has no way to tell the yard's
script from the edge's rewrite of it, because its trust anchor is the
edge's certificate. Previews stand the same way on their own hosts.

Peers are carried through the edge when a yard is called at
`https://my.yardr.dev/peers/<account>/<yard>` (`docs/edge.md`;
`docs/peers.md`): only `/peers/messages` and the three `/peers/pair/*`
paths, passed down the link and answered back, read as a webhook is
read — the peer signatures (`SignRequest` and its answer's signature
in `internal/yardkey/yardkey.go`) prove who sent what and hide nothing.
Two yards already each hold an ed25519 key pair (`Ensure` in the same
file, the seed in a 0600 file), and a link between them is saved under
the other yard's public key.

Webhooks cannot be end to end, and the bead leaves them out: GitHub and
the rest post plaintext to the edge, the edge queues it (at most 1 MiB a
delivery, bounded per yard and across the edge), and the yard verifies
every delivery itself with secrets the edge never holds. A hosted yard
collapses the question from the other side: the yard runs on the
service's box, its key file on that box, so encryption between a device
and that yard protects the device's words from the edge process only
as far as the box's operator chooses not to read the yard beside it.

### 2.7. What the edge keeps for itself, and what it logs

The router resolves a device call from the Kratos session and the map
(the provisioner's SQLite file of accounts and yards, each connected
row holding the yard's public key for enrolment and signed calls). With
`EDGE_LINK_LOG=1`, which the deployment sets, the edge writes one line
per link request when it ends: the yard's ref, the request id (also the
answer's `X-Edge-Request` header, which footplate quotes in its own log
so one id finds all three places), method and path — the query left out
because it can carry a token — status, times and an outcome word
(`answered`, `timed-out`, `link-down`, `refused by=yard`, and the rest,
router README, "What the edge logs of a link request"). No body, header,
cookie or token is logged. The edge's public notice at `/edge/notice`
(`noticePage`, router README, "The edge's notice") is the person's side
of the same ledger: what passes through the edge and can be read there
— webhook payloads, footplate calls, footplate's device token, a
login's one-time sign-in code, an agent session's screen, the session
and the web view — what it keeps and for how long, and what it never
holds. Any E2E pick that changes that page's truth changes the page in
the same bead; the bead that asks this question changes neither page.

## 3. The proposal

The smallest version that answers the question is one new channel
between a paired device and its yard, and nothing else. At pairing, the
device makes itself a key pair for that yard and learns the yard's key
from a place the edge cannot rewrite (the pairing QR, section 3.1);
from then on, every device call through the edge is one encrypted
envelope inside the call the edge already carries, the edge routes the
envelope by the yard it already names, and the yard opens it where its
listener already stands. The yard's existing keys, the link's existing
frames, the store's existing device record and footplate's existing
per-yard files carry it; what is new is a Diffie-Hellman channel key
per device pairing, one envelope format, and the refusal and notice
words that keep the edge's ledger true. Sections 3.1 to 3.7 take the
bead's seven questions in order, each with its candidates, what each
costs, where it fails and what it assumes, and the writer's ranking
last. Section 3.8 states the threat model those rankings rest on, and
section 3.9 shows what a person would type and see. Seven of the picks
are no longer the writer's alone: mckean decided the founding
ceremony, the plain-call rule, secrets, migration, the notice's
sentence, the Noise implementation and the phone library's review
rule on 5 November 2026, through
the bead's asks. Where a ranking below ends in one of those picks,
it says so; section 5 records each pick against the options it was
chosen from.

A note on mechanism before the candidates. Yardr's three tests
(`README.md`, "Principles") are generalization, composability and
simplicity: prefer a mechanism that exists over a new one, and a
general one over a special case. The yard already signs with ed25519
and already pairs twice — devices by a bare code, yards by a code plus
a compared check. A channel design that needs a new identity, a new
server, or a new ceremony for each surface (device, peer, web) fails
that test before its cryptography is judged. The candidates below are
scored against it as well as against an edge that first merely reads,
and then actively relays and rewrites (section 3.8).

### 3.1. Question 1: key agreement the edge cannot learn

How do a device and a yard get a shared key when every byte of their
first exchange crosses an edge that may relay and rewrite it? Four
candidates, and one dismissed at the door.

**1A. A PAKE over the pairing code (SPAKE2 or CPace).** The device and
the yard run a password-authenticated key exchange with the pairing
code as the password, relayed by the edge like any other pairing
traffic, and exchange their long-term public keys under the key it
derives. A PAKE's defining property is that someone who controls the
channel but not the password gets one online guess per run and no
offline test at all: a transcript cannot be brute-forced afterwards,
which is the failure of the yard's own peer-pairing HMAC, whose
transcript a reader can try codes against at PBKDF2 speed. SPAKE2 is
RFC 9382; CPace, the other candidate named for this shape, is still
an Internet-Draft ([draft-irtf-cfrg-cpace](https://datatracker.ietf.org/doc/draft-irtf-cfrg-cpace/),
at revision 21 and still active when checked on 5 November 2026). What it costs: a correct, reviewed PAKE implementation in Go
and in each device language where one does not already stand — neither
is in Go's standard library or in `golang.org/x/crypto` (the module's
one crypto dependency today), and a hand-rolled PAKE is exactly the
cryptography a project should not write for itself. Where it fails:
an edge that guesses right on its one try in a run becomes the yard
for that pairing — with about 40 bits of code and the yard's five-try
ending, the arithmetic is the pairing's existing arithmetic, no worse
— and a person who is shown the code by the edge's own page rather
than by their yard has handed the password to the channel. What it
assumes: that the code reaches the device over a way the edge does not
read (the yard's screen, a terminal, a QR the yard itself draws), and
nothing about certificates, cameras or a second person. A pairing
whose code the edge itself hands out fails that assumption before the
exchange begins: in the member's pairing the router reads the yard's
code and returns it to the member's session (router README, "The
member's pairing"), so the edge knows the PAKE password and can sit
in that exchange as either end. No PAKE over that code gives a member
the sealed claim; section 3.1's ranking keeps members on the QR.

**1B. The yard's public key shown out of band (the pairing QR).** The
yard already draws a QR at pairing (`yardr user device pair --qr`,
`docs/users.md`): it holds `<address>#<code>` and, through the edge,
`<address>#<code>#<yard>`. The QR gains a fourth field, the yard's
channel public key, shown on the yard's own machine where the code is
shown. The device pins that key, opens a Noise handshake to it through
the edge (candidate 2B's channel), and redeems the code inside the
handshake, so the edge never sees the code, the token it buys, or any
later key. What it costs: a longer QR (a 32-byte key is 43 characters
of base64url, well inside what the terminal QR already carries), a
channel key pair on the yard beside its signing key — an X25519 static
whose public half the yard's ed25519 identity signs, so a device can
also learn it from the edge later and check it against the key the QR
pinned — and a yard-key change becoming a re-pairing event, as a
device-token change already is. Where it fails: where there is no QR
to scan. Footplate on a desktop pastes the QR's text, which carries
the key as well as the code, so the pasted path is covered; a code
read aloud and typed has nothing out of band but the code itself,
which is candidate 1A's ground. A member's pairing opened from the
app through the edge (router README, "The member's pairing") is not
1A's ground at all: the router itself reads the yard's code there and
returns it to the member's session, so the edge holds the would-be
password and a PAKE over it proves nothing against the edge. A member
gets the sealed claim only the 1B way — the yard's channel key shown
on a screen the yard itself draws (its QR, scanned or its text pasted)
— and an edge-independent member ceremony, one whose secret never
crosses the router, is a later bead of its own, not a promise of bead
16. The member's self-service pairing page is the same case in the
web view: today a signed-in member opens a pairing there and scans
the QR it shows (`internal/web/pairing.go`, which opens the pairing
and serves its QR, and `internal/web/templates/pairing.html`, which
presents it), but that page and its QR reach the member through the
edge, so the edge can read or rewrite the code and the key the QR
would carry. Under `require` (section 3.5) that page can therefore
no longer pair a member's app that calls through the edge. The
member pairs instead by a QR the operator opens on the yard's own
screen, and the page must tell the member exactly that: that pairing
from this page no longer gives an app that can call through the
edge, and to ask the yard's operator to open a pairing QR on the
yard's own screen and scan that. That loss is accepted for now
(mckean's pick on ask 20536, 5 November 2026): the member pairs by
the operator-opened QR, the page says so in those words, and a
self-service member ceremony is a later design of its own, named
as left out in section 4; this design gives none. What it
assumes: that the person
scanning is looking at their own yard's screen — the same assumption
pairing already makes about the code, now carrying the key with it.

**1C. The peer pairing's check, carried to devices.** Reuse the
exchange of `docs/peers.md` whole: device and yard commit and reveal
key-bearing nonces under a code-keyed HMAC, each derives the same
six-digit check over everything that crossed, and the person compares
the yard terminal's digits with the phone's before either side saves
the other's key. What it costs: no new cryptography at all — SHA-256,
HMAC, PBKDF2 and ed25519 from the standard library, in code the yard
already runs for yards — and one new step a person can get wrong.
Where it fails: exactly where its parent fails. The check is the
line, and a flow that lets a person skip it (an `--yes` for devices)
gives the line up; a pairing driven from the web pairing page, where
the yard's half of the check must be shown by the edge's page or the
web view, asks the person to compare digits one of which the channel
under judgement supplied. What it assumes: two screens in one person's
hands at one moment, which device pairing usually has and a member's
remote pairing may not.

**1D. Pin after a plain pairing (trust on first use).** Pair exactly
as today; under the token that pairing issues, the device registers a
static public key with the yard and the yard returns its own, and
every later call is encrypted to the pinned keys. What it costs:
almost nothing — no ceremony change, no new person's step, migration
for free. Where it fails: on the only day it matters most. An edge
that is merely logging gains the code and the token at pairing and
loses nothing by waiting; an edge actively rewriting that one
exchange substitutes its own keys and holds the channel for the
pairing's life, undetectably. It protects every call after an honest
pairing, which is a real gain against a log kept and read later, and
it is the only candidate that does. What it assumes: that the edge was
honest, or at least passive, during each device's one pairing — an
assumption the bead's question ("which survives an edge that relays
and rewrites the pairing") rules out as the design's foundation,
though not as a migration step (section 3.4).

Dismissed: bootstrapping from the edge session itself — deriving the
channel key from anything the Kratos session or the edge's enrolment
hands out. The edge mints and checks those; a key it can derive is a
key it holds, whatever the derivation is called.

**Ranking, question 1 — picked: 1B now, 1A next.** First, 1B, the
QR-pinned yard key with the code redeemed inside the handshake: it
is the only candidate that survives a relaying, rewriting edge on
the pairing people actually perform, with no new cryptographic
primitive of our own — a Noise handshake to a pinned static key,
run by a maintained library in each language (section 3.2) — and it
extends a ceremony that exists (`--qr`, already printed at the
yard's own screen) instead of inventing one. Second, and decided as
the next slice (beads 16 to 19, one per repository), 1A, a PAKE
over the code: the strongest
answer where no QR can be scanned, for typed and read-aloud codes
whose code never crossed the edge, held to second only by the cost
of a reviewed PAKE library in four codebases, under the same
no-own-crypto rule as Noise. Member pairings opened through the app
are outside beads 16 to 19's guarantee, for the reason 1A and 1B state: the
router reads and returns that code, so the edge knows the password.
A member is sealed by 1B — the yard's key from the yard's own screen
— and a member ceremony whose secret the router never sees is a
later bead only, named as such and not built here. That ranking
carries the loss stated under 1B above: the web view's self-service
pairing page (`internal/web/pairing.go`,
`internal/web/templates/pairing.html`) no longer pairs a member's
app for calls through the edge, the member pairs by a QR the
operator opens on the yard's own screen, and the page must say so
in those words; that loss is accepted for now (ask 20536), and a
self-service member ceremony is a later design, not this one.
Third, 1C, the compared check: sound, already built for
yards, and the fallback if a PAKE proves unobtainable — ranked
below the PAKE because it spends a person's attention, and spent
attention is where pairings fail. Last as a foundation, first as a
migration, 1D: plain pairing plus pinning is how existing devices
get keys without a ceremony (section 3.4), and no device should be
described as protected on its word alone.

### 3.2. Question 2: the channel, and what the edge must still read

Given a pinned yard key and a per-pairing device key, what is
encrypted, and how does it ride the frames that exist? Three
candidates for scope, then one for shape.

**2A. The body only.** The call's method, path and headers cross as
today; the body is sealed with an AEAD under the channel key. The
edge keeps everything it does with a call today — routing, the 1 MiB
and 16 MiB bounds read off the lengths it carries, the link log's
method and path, a 401 it can still tell from its own refusals — and
`GiveSecret`'s value, inside a body, is hidden. What it costs: the
least new mechanism anywhere in this design; an envelope in one body,
on one path, per call. Where it fails: the edge still reads the shape
of a life. Paths name the surface (`/user/call/ApproveAsk` and its
ask, a blob's fetch, the events stream), headers name the device token
unless that too moves, and sizes and times name the rest; a reader who
cannot open a single letter can still keep the diary of when the
house woke, what it approved and how large its screens were. It also
leaves the token where it is — in a header the edge must carry — so a
compromised edge keeps its impersonation whole. What it assumes: that
content, not metadata, is the harm. For a harness sign-in code that is
true; for the bead's stated end — calls the edge cannot read — it is
half an answer wearing the whole name.

**2B. Body, path and headers inside one envelope; routing outside.**
The device serialises the call it would have sent — method, path,
headers (its credential now the channel key, section 3.3), body — and seals
the whole as the body of one outer call on one fixed path,
`/user/e2e`, named to the same yard by the same `X-Edge-Yard`. The
yard opens the envelope where its link hands it the frame and serves
the inner call exactly as its listener serves it today, then seals
the answer the same way. What the edge must still read, and all it
reads: which yard (the routing name it cannot do without, the account
its session check already names), which link and role, that a frame
is a request, a response head, a stream piece or an end, the sealed
length (so its 1 MiB frame, 16 MiB answer, 64-call and 64 MiB queue
bounds hold on ciphertext, with the plaintext budget lower by the
envelope's overhead), one outer stream marker and one outer call
identifier described below, and
the stream's record lengths and silence, against which it writes
beat records of its own devising to the device, also described below.
`X-Edge-Presenter` stays outside too: it is the edge's word
about its own session, the yard takes it only on the pairing call
today and only to narrow who redeems, and sealing it would ask the
device to vouch for the edge's account-keeping. The edge
session does too, for the opposite reason: `X-Edge-Token` (the
Kratos session the router checks to resolve the account before it
can route, section 2.2) stays outside the envelope in the clear,
read by the edge and by Cloudflare on every enveloped call as it
is today, and stripped by the router before forwarding, so the
yard never sees it either way. The credential that leaves the
clear is the other one: the device token in `Authorization`,
which today the edge reads and could replay as the device, does
not cross on an enveloped call at all — the channel key
authenticates the call instead (section 3.3) — so nothing the
edge reads on such a call lets it act as the device. The two
must be kept apart wherever the design says `token` (sections
3.8 and 3.9): sealing hides the device token; it does not hide
the edge session. The link's `ping` and
`pong`, the queue's ack and the challenge/hello stand untouched: they
are the yard's and the edge's conversation about the link, they carry
no device words. The frames that carry envelopes stay signed by
the edge to the yard as every request frame is today (`SignFrame` in
`internal/yardkey/yardkey.go`; the yard's check in `Link.check`,
`internal/edgelink/link.go`). That warrants an outsider's forgery
and a replay, not the edge: the edge signs every frame it sends, so
a frame from anyone without its key, and a frame sent again on a
later link or out of order, are refused before the yard spends a
decryption, while the edge that holds the key can mint a correctly
signed frame at will. What refuses it there is the envelope, not
the signature: an invalid envelope it submits passes the outer
check and fails the AEAD opening below, which needs the session
keys it never learns, so it costs a failed call and nothing more.
Where it fails: an active edge can still drop, delay, duplicate outer
frames (the channel's call-bound explicit sequences answer
duplication inside a session, section 3.8), and measure everything 2A leaks but the
names. What it assumes: one fixed outer path is enough — no edge
feature needs the
inner path, once the push probe is answered another way (section 3.3).

**2C. A second, encrypted link.** A new WebSocket from each device,
through the edge as a blind byte relay, to the yard, carrying a Noise
stream the edge never frames. This is Tailscale's DERP shape and Home
Assistant's SNI shape (section 3.10): the relay forwards sealed bytes
and cannot read them by construction. What it costs: a second
connection discipline beside the link — presence, reconnection, the
64-call accounting, streamed answers, the events beat, session-ending
mid-stream, all rebuilt where the byte pipe crosses the router and
again where it lands in the yard's server — to buy what 2B buys inside
machinery that already does each of those. Where it fails: nowhere
cryptographically; it fails yardr's own test. It is a general mechanism
duplicated, where the envelope is the same mechanism composed with
the link that stands.

Two pieces of outer framing make 2B work with the path hidden, and
each is specified here because the router cannot guess either from
the ciphertext.

**The outer stream marker.** Today the router knows the one call
that may sit idle for hours only because it can read the path:
`eventsStream` in the router is true of `GET /user/events` alone, and
an enveloped events call, its path sealed, would fall to the ordinary
streamed-answer handling, which has no beat and cuts an answer that
writes nothing for one call wait (30 seconds). The device therefore
marks the outer call itself, in the clear, with one header the router
reads before framing the call for the link —
`X-Edge-E2E-Stream: long-lived` — set exactly when the inner answer
may legitimately sit idle without bound, which today is the events
stream and nothing else. The router drops the device's header (the
inner call never sees it, as it drops the headers it strips today)
and copies the marker's presence and value into the link frame's
header beside the call id described below, so the yard can bind the
marker the device sent into the first record's associated data (the
shape paragraph below) without ever scheduling on it. A marked outer
call is opened as a stream on the link and held under the events
rules (no deadline on the head or the stream, the session looked at
again as it runs, every write through the idle bound); an unmarked
one is delivered as an ordinary call and, if its answer streams, cut
after a call wait with no piece, exactly as a streamed blob is today.
The marker is scheduling only at the router: it authorises
nothing there. At the yard it is checked, not trusted. After the
yard opens the envelope — and only then, since the inner call is
sealed until that moment — it rejects a marked call whose inner
call is any other than `GET /user/events`, and it counts every
marked call it accepts against the existing per-credential stream
cap (`StreamsPerCredential`, 16, in `internal/rpc/user.go`: the
same cap the plain events stream already counts against), so a
device that marks an ordinary call does not buy itself an
uncapped long-lived slot, a bypass of the router's call deadline
at the yard, or one more of the link's 64 open calls than the cap
allows; the refusal is sealed inside the envelope as any inner
refusal is. An unmarked events call is no exception in the other
direction: the router has already delivered it as an ordinary
call, and it is cut as one. The yard's check is what makes the
router's one-bit hint safe to act on for scheduling.
What the marker gives away is one bit, given away early: that this
enveloped call belongs to the long-lived class. The edge would learn
as much unaided by watching a connection outlive one call wait
without a byte, and since the class has one member today it can guess
the events stream from that, as it can already guess it from one long
connection opened beside an app's short calls. It learns no inner
path, method or name from the bit, and a second long-lived call,
should one ever exist, shares the bit and is indistinguishable under
it. The two ways of not paying even that bit were considered and not
taken: giving every enveloped stream the events rules hides the class
and loses the stalled-blob cut, so one stuck answer would hold its
call slot and queue room until the device or the link itself gave up;
and a second outer path for long-lived calls names the events stream
outright and freezes that name into the edge's routes.

**Answers are records, and the beat is an empty one.** A sealed
stream piece is ciphertext, which may hold any byte, so the empty
line the router writes today cannot cross inside it: a device that
tried to open that line as a sealed piece would refuse its own
stream. Every transport payload therefore travels as a run of
records. Each record is a four-byte big-endian length followed by
that many bytes of one transport message (the shape paragraph
below); the sender chops its plaintext — a whole call too large for
one message, a whole answer, a streamed answer as it flushes — so
no transport message passes the Noise bound of 65,535 bytes, its
clear header and its tag counted, and the receiver joins the opened
chunks in record order. The link's own habit of splitting a flushed
piece across data frames is harmless, because the record's length,
not the frame, says where a message ends. A record of length zero is
the beat, and only the router ever writes one, only on the device's
leg of a streamed answer: it writes one, and flushes it, whenever it
has written nothing to the device for one events beat (30 seconds),
rearming before the write as it does today, and only at a record
boundary, which during
real silence is where it already stands — a record half-carried when
the beat falls due is finished first, since bytes moving are the
liveness the beat stands in for. Nothing is sent to the yard for a
beat; the link's own `ping` and `pong` are unchanged. The device
discards zero-length records before any decryption and reads them as
it reads today's empty line: proof that its connection to the edge
is alive, and no word at all about the yard. A zero-length record is
router framing, not a transport message: it carries no associated
data and no ciphertext, and neither end seals or opens one, so there
is nothing for an AEAD to bind. A compromised edge or Cloudflare that
forges one, drops the router's, or replays one therefore touches
availability only — a stream may be kept seemingly alive past its
cut, or, by withholding beats, let be cut — and can inject, alter or
reveal no inner word by it; the next real record must still open
under its own sequence, index and final-record checks before a byte
of it is served. What the framing shows
the edge is the length and boundary of each sealed record, which its
link frames and its own writes already show to within a frame's
padding, the number of records, which its frame count already tells,
and the beat's cadence, which is its own. Its cost is four bytes a
record and a length-counting step in the router's stream writer,
small change beside the idle bound that writer already keeps.

For shape, one candidate is proposed over its rivals. The channel is a
Noise handshake (the Noise Protocol Framework, revision 34) opened
lazily when an app starts speaking and kept for the app's connection:
`Noise_IK` where the device already pins the yard's static key (every
post-pairing start), falling back to `Noise_XX` with the static key
checked against the pin where it does not, X25519 with ChaChaPoly and
SHA-256. No yard writes the Noise constructions itself (picked,
question 6): `golang.org/x/crypto`, the module's one crypto
dependency, carries primitives and no Noise package at all
(`go doc golang.org/x/crypto/noise` finds none), so each codebase
takes a maintained Noise library, checked on 5 November 2026, and
holds it against the specification's test vectors and against the
others before it ships. The yard (Go) takes
`github.com/flynn/noise` at v1.1.0: tagged, not archived, no commit
since February 2024 — stable and slow-moving rather than active.
The desktop app (Rust) takes `snow` at 0.10.0 (19 July 2025):
actively maintained, commits into 2026, tracking revision 34, and
carrying its README's own warning that it has not received any
formal audit — accepted for the desktop under question 6's
maintained-library rule, not under an audited claim. The phones
are held to the stricter rule mckean picked for them (find an
audited library first, section 5, Q7), and under that rule no
native phone library qualifies. Weighed on 5 November 2026:
`stonygrey/torann` at 0.3.1 — `Torann` for Swift (iOS 18 and
later, which is footplate-ios's own floor) and
`com.stonygrey:torann` for Kotlin on Maven Central, sharing one
API and one set of test vectors, actively developed (last pushed
October 2026) — states in its own README that it is not
production ready and has not been independently audited, and its
Kotlin package asks Android 13 (API 33) and later against
footplate-android's floor of API 29; the smaller native libraries
beside it carry no audit statement at all. Bridging `snow`
through its foreign-function interface would give the phones the
desktop's code, but it moves the same no-formal-audit warning
onto the phones and so does not meet the audited-first rule
either. Bridging the yard's own library is the closest path, and
its review is narrower than that path needs. Cure53's Turbo
Tunnel review (March 2021, report dated 14 April 2021) was a
white-box review of `dnstt`, with `flynn/noise` examined as used
there; its `flynn/noise` findings were UCB-02-003 (nonce
overflow) and UCB-02-004 (the deprecated Curve25519 API), fixed
in `flynn/noise` v1.0.0. That review covers neither the
v1.1.0 the yard pins — which adds `UnsafeNewCipherState` — nor
the phone binding below, nor this design's explicit-nonce and
replay framing (the per-message `SetNonce` copies, the call-bound
associated data and the opened-sequence window of this section),
none of which the `dnstt` use exercised. The bound phones are
therefore not called audited in this design. Picked as the
mechanism for the phones, subject to that limit: the same
`flynn/noise` v1.1.0, bound from Go into Swift and Kotlin with
`golang.org/x/mobile`'s `gomobile bind` (Android and iOS
targets), so each phone app runs the yard's Noise code byte for
byte and the binding we own marshals bytes and holds no Noise
construction of its own. Its cost is a Go runtime and a
`gomobile` build step inside each phone build, proved on the
phones' own floors (iOS 18, API 29) by shared test vectors and a
live handshake against the yard and the desktop in each phone
bead — proofs of agreement, not of review. What the bound
phones would still need is an independent review of
`flynn/noise` v1.1.0 as bound by `gomobile` and of the
explicit-nonce and replay framing the binding carries. Picked
(mckean, ask 20524): the phone beads build now on this bound
v1.1.0, and the phones' sealed claim is gated on that review.
Until an outside review of `flynn/noise` v1.1.0 as bound by
`gomobile` and of that framing is done, a phone shows its device
as not yet sealed, its app makes no sealed claim for it, and no
public text claims more; the review is its own later step, and
its passing is what lets a phone say sealed. Beads 7 and 8 build
on those words and make no audited claim. Torann stays
only as a fallback, marked unaudited: if the bound Go artifact
cannot be built, kept within the store's rules, or held on
those floors, phone work does not start on Torann or on any
other unaudited code — it waits for an independent review of
the fallback, or for another audited library, and no sealed
claim is made for phones until then.
Writing Noise constructions to escape the choice is not an
option in any bead. A per-call sealed box to the
yard's static key (no handshake, no state) was considered and not
taken: it has no forward secrecy — the yard's one static private key
opens every call ever logged — and no place to bind a device's reply
key but a header it would expose. Holding one session per yard in
each app, beside the connection each app already holds open for its
events stream, costs one handshake round trip at a start and none per
call. A handshake is the one exchange that still rides its own
implicit counters, and it is run in order, once, before the session
carries anything else, so it shares nothing with the concurrency
below. Because the edge's calls are one-shot, an envelope names its
channel session in the clear outside the ciphertext — a random tag
of sixteen bytes the handshake mints — so the yard can find the
session's keys where it keeps them, in memory; a yard or app restart
ends sessions, and the next envelope is a fresh handshake, which the
tag's absence announces. The tag joins a device's calls within one
app start, as the account session already joins them at the edge.
The session's entry names more than its keys: the user and device
whose pinned static key the handshake proved, and the calls live
under it. No entry is made for a key no live device record pins, and
an entry is a cached credential, not a copy of the record: what
removal does to it is specified with the replay rule below, because
a session that outlived its device would make every refusal built on
that record a promise the channel does not keep.

Transport messages cannot ride the handshake's implicit counters,
and the reason is already in the yard: a `CipherState` (Noise section
5.1) seals with its key and a counter `n` that each message advances,
so messages under one state must be sealed and opened in one order,
while the yard checks link frames in the order they came and then
serves each request in a goroutine of its own, and the link holds 64
of a device's calls and streams open at once. Two honest requests,
sealed under one counter in one order, reach the yard's serving
goroutines in another, and the second to be opened is refused for the
first's offence; two stream pieces of different streams interleave
the same way. Locking one state at both ends would not repair this —
the lock orders the sealing, not the arrival — and it would queue
every call behind the slowest opening. The messages therefore carry
their number with them, which is the handling Noise itself prescribes
for out-of-order transport (section 11.4). Numbering alone is not
enough, because the two directions share one key pair across every
call the session carries: a sequence tells a receiver which nonce a
message claims, not which call it belongs to. Every message
therefore also carries the identity of its call. The device mints
that identity — sixteen random bytes, the call id — once for each
call it opens, the events stream counting as one call for its whole
life, never reuses one inside a session, and names it in two places
that must agree and a third built from one of them: on the outer
call, as the header
`X-Edge-E2E-Call`; in the clear header of every record of the call
and of its answer; and in the AEAD's associated data, which each
sender builds from that clear header, so a header rewritten in
transit fails the opening it was rewritten for. The outer header is a claim
to be checked, never a warrant: what proves a call id is that a
record sealed under it opens, exactly as a frame's signature, and
not the header, proves a frame is the edge's. A transport message on
the wire is the session tag, then the call id, then an eight-byte
big-endian sequence, then an eight-byte record index that counts
this call's records in this direction from zero, then the
ciphertext, and the tag, a direction byte, the call id, the
sequence and the index are the AEAD's associated data,
so none of them can be rewritten or swapped between sessions or
between calls without the opening failing, and a message reflected
into the other direction fails under the other key as well. Two
outer fields join the associated data where binding them costs no
transmission. The first record of each call in each direction also
binds the fixed outer path, the literal `/user/e2e`: both ends know
it without it crossing (the device addresses it, the yard's frame
names it), so a sealed call replayed onto any other outer path fails
its first opening. The same first record binds the outer stream
marker's presence and value: the device seals what it sent, the yard
seals and checks what the router copied into the frame header beside
the call id, so a marker added, dropped or rewritten between the
device and the yard fails that opening and the call with it, rather
than silently reclassifying the stream; later records need no second
binding, since the marker belongs to the outer call, not to a piece.
What stays outside the associated data is stated as plainly: the
zero-length beat records (router framing with no message to bind, as
above), the outer frame kind, lengths and timing, and the link's own
`ping` and `pong`. Forging, dropping or delaying any of those touches
availability only — a stream may end early or linger, a call may fail
whole — and alters or reveals no inner byte, because every inner byte
still crosses only inside a record that must open under the bound
fields above. The
call id in the clear groups one call's records and joins them to
their answer, which the edge's own frame and connection already do;
being random per call it joins nothing across sessions or yards.
Each direction numbers its own messages from zero under an
atomic allocator, a number used once and never again in the session:
to seal, the sender takes the next number, copies its sending key
into a fresh `CipherState`, sets that number with `SetNonce` and
seals; to open, the receiver does the same with its receiving key and
the number the message carries. After the handshake the two keys are
never mutated, only copied, so any number of calls and stream pieces
seal and open at once, none waiting on another — the 64-way
concurrency the link already allows is the channel's, not a queue
the channel imposes. A sequence that runs to the reserved maximum
(2^64-1) ends the session by the specification's own rule and the
next envelope handshakes again, as at a restart; at the record bound
above that is some exabytes away. The sequence in the clear tells the
edge how many messages a session has carried and in what order they
were sealed, which its own frame count and the session tag already
tell it.

What rides inside the records is the inner call, whole. The joined
plaintext of a request is its head — method, path and headers, as a
length-prefixed block — followed by its body; the joined plaintext
of an answer is its head — status and headers, the same way —
followed by its body. The inner status and the inner headers are
therefore sealed with everything else: the head the link carries
back (`responseFrame` in `internal/edgelink/link.go`, whose status
and headers the edge reads today) carries only the outer answer —
status 200 when the yard sealed an answer, whatever status the
inner answer holds, and the link's own refusals (yard down, queue
full) as today — with headers that name the envelope and the
request's id and nothing of the inner call's. The edge can tell
that a sealed answer arrived, and cannot tell an inner 403 from an
inner 200; a refusal inside the envelope is explained by the yard
to the device inside the envelope, and by the yard's own log to its
operator. The inner head counts toward the answer's 16 MiB bound
and toward its record budget, as the body does.

Every call ends with a final record. Its index is the count of the
call's data records in its direction, and its sealed plaintext is
sixteen bytes: that count again, and the total length of the data
records' plaintext. A receiver holds a call complete only when the
final record opens and both numbers match what arrived before it.
A record whose index is not the next one its call expects is a
gap, not a reordering to be joined across: records past a gap are
not delivered as if the joined plaintext were whole — for a whole
call or answer, nothing is served or shown until the final record
closes it, and for a streamed answer the records before the gap
stand delivered and the stream then reports its cut, as a cut
plain stream does today; a consumer that needs the whole (a blob
being saved) discards the part, as its sealed head's own length
already tells it to. The outer stream's own ending — the link's
`end` frame, the HTTP answer's close — without a final record is a
truncation, reported the same way, never read as a short complete
answer. The yard serves an inner call only after its request's
final record opens, so a truncated request is refused unserved;
the events stream's final record comes only when its own side
ends it, and an outer ending without one is the cut stream a
client already reopens.

A receiver checks a record against its call before it spends an
opening on it, and the two ends come by the expected call id
differently. The device knows its own: it minted the id for the call
whose answer it is waiting on, so an answer record whose header
names any other call is refused on the connection it arrived on
without being opened. The yard learns its expectation before any
goroutine can open a record: the router copies `X-Edge-E2E-Call`
into the link request frame's header beside the path it already
carries, and `Link.serve` (`internal/edgelink/link.go`), which reads
frames in order and checks each before handing it to a goroutine,
registers the frame's id against the call id the
frame claims — the same ordered place that already holds each live
request's cancel by id. The serving goroutine then requires every
record's clear header to name its own frame's registered call; a
call id already registered to a live frame on that link cannot be
registered to a second, so two concurrent frames cannot come to
share one identity. (A closed call's mark, below, is kept by its
own session's receiver, which is where a record naming it arrives.)
The edge can rewrite the outer claim it carries, but the rewrite is
only ever a refusal: it either disagrees with the sealed header that
the associated data binds, fails the opening, or collides with a
registration already made, and in each case nothing is opened and
nothing is marked for the honest record that may still arrive on
its own call. Transposition is therefore not a matter of whose
replay mark a record reaches first; a record carried to the wrong
call is not that call's record at all.

Replay is judged on numbers that have opened, never on numbers
merely seen, so a forged or transposed message cannot burn the
honest one that carries its number after it. The judge is one
window for the whole session in each direction, not a mark per
stream: because every record in a direction opens under the same
key, its sequence is a nonce under that key, and Noise's rule for
such transport (section 11.4) is that a nonce once successfully
opened is never accepted again. A receiver therefore keeps a
sliding window of the last 65,536 opened sequences in that
direction as a bitmap — a size set against the largest burst that
can be in flight at once, counted from bounds that already stand.
A sender chops plaintext into full-size records (the 65,535-byte
Noise bound, tag counted) and seals a smaller one only at a serving
flush or at a call's end; a full 16 MiB answer is then 257 records,
a full 1 MiB request 17, and 64 of each at once — the link's own
concurrency bound — are 16,448 and 1,088 records, so the window
holds about four times the largest burst one link can present
before its receivers open it. The window refuses any record, of a
call or of a stream, whose sequence is already marked there, for
whichever call it now names, or has fallen below the window. A
stream keeps a second, narrower mark beside the window: the next
record index its call expects, which its records must meet exactly,
since records of one stream are sealed in the order the yard
flushes them and carried in that order — one link socket, one
stream on the router, one answer on the device's connection. The
two marks answer different questions, and neither substitutes for
the other: the per-call index orders and completes a stream (it is
what the final record's count is checked against), and the window
is what stops a record of a closed stream, or of any call, being
opened a second time under the session's key on a different call
whose own index it would pass. What a lagging stream needs is not
an exemption from the window but the allocator's habit above: a
stream's next record takes the session's next sequence when it is
sealed, however long the stream sat silent, so silence moves no
record towards the window's edge; only a record held back in
transit past 65,536 later openings falls out, and refusing it is
the correct answer to a delay no honest carrier imposes. A call
whose record falls out fails as a whole, and is never resumed in
the middle: it is issued again under a new call id (safe where the
refused record was a request the yard never opened, and for a lost
answer to an action the app reports the answer lost rather than
issuing the action again), and a stream is re-opened, not replayed. When a
call or stream closes, its call id is kept as a closed mark with
its last opened sequence until the window's highest opened sequence
passes that last one by more than the window's width, when every
sequence the call could name has fallen out and the window alone
refuses its records; until then a record naming a closed call is
refused however fresh its sequence looks. The marks live and die
with the session's keys in memory, and a replay across sessions
fails on the tag before it reaches them. The cost of all of it is
forty-eight clear bytes a message, a key copy a message — a
ChaChaPoly key is thirty-two bytes, copied, not re-derived — eight
kilobytes of window a direction, sixteen bytes a stream while it
is open, and a closed call's id and mark for as long as the window
remembers its sequences.

Removal reaches the sessions a device founded, at once. A session
entry is made only against a live device record: the handshake's
static key must still be the key that record pins, and the entry
names that record's user and device. The first record of every new
enveloped call re-reads that record from the store and refuses the
call — and drops the entry, cancelling the device's other live
calls as below — when the record is gone or pins another key, so a
call on an established session is judged against the device list as
it stands when the call begins, not as it stood when the app
opened. One store read a call is the price, and it is the read a
plain call already pays to resolve its token. Records inside a
call already begun are covered the other way, by cancellation
rather than by reading: `RemoveDevice`
(`internal/store/devices.go`) already writes a `device_removed`
event as it commits a removal, and a user's removal writes its own
(`user_removed`, in `internal/store/users.go`) naming the
devices it drops with it; the server's standing event watch
(`internal/server/edge.go`, which already subscribes to the store's
events and reacts to one kind there) gains a branch for each: on
either event it has the yard's current link purge every session entry
naming that user and device and cancel each of its live
calls and streams through the link's existing tracked cancels
(`track` and `cancel` in `internal/edgelink/link.go`), so an events
stream ends and a handler serving a half-made call loses its
context in the removal's own moment. The purge follows the
store's commit, and the first-record read above is the backstop
that closes the race between the two: a call whose first record
crosses the commit finds no device, whatever the purge has yet to
reach. An answer half-sealed when the purge lands is never
finished: the yard finds its session entry again before it seals
each answer record, and an entry that is gone ends the answer with
the link's refusal rather than with a new record, so the yard seals
nothing new for the device from the removal on. Removal cannot
recall what it already sealed: a record sealed before the purge
and already on the link, in the edge's hands or on the device's
connection, still opens at the device, which holds the session's
keys until it learns of the removal. The accepted limit is the
answers of the device's calls live at that moment — no more than
the link's 64 open calls, each bounded at 16 MiB, and in practice
the one answer a live call was returning — all of it plaintext the
yard had already answered while the device stood authorised: an
approval's result, a screen, a secret the device had just been
given. A removed device learns no new words after removal; it may
finish hearing the sentence it had already been sent. The device, on
a refusal that names its session gone, discards the session and its
token and asks to be paired again; a fresh handshake from its key
is refused at the yard, which pins it no longer, so the discarded
state cannot quietly regrow. Sessions of every other device — of
the same user, and of every other — are untouched: the purge is
keyed by the one user and device the removal named, as the token
refusal always was.

**Ranking, question 2.** First, 2B in the Noise shape above: it is the
smallest candidate under which the sentence "the edge carries calls it
cannot read" is true of the call and not only of its body, and it asks
the router for little: one path it passes on like any other, one
marker it reads, copies beside the call id for the yard's
associated-data check and drops as a device header, one call header
it copies into the frame unchanged, and a beat it writes as an empty
record instead of an empty line.
Second, 2A, body only, as a deliberate half-step if a first slice must
land before the push probe is replaced: it hides every secret value
and every letter, and its notice words must then say body and not
call. Last, 2C: right for a relay designed around byte pipes, wrong
for one designed around signed frames.

### 3.3. Question 3: what breaks or changes

**The push register probe.** It is the only place the edge reads a
device answer on purpose, and under 2B it cannot: the probe's answer
would be sealed to the device, not to the router that asked. Three
candidates. (a) Keep the probe in the clear as a named exception: one
`GET /user/me` per registration stays readable, and the notice keeps
one honest line saying so; the cost is a permanent hole shaped exactly
like the metadata the channel hides, and a token the edge must still
be handed for that one call. (b) A yard-signed registration voucher:
at pairing, and afterwards on request inside the channel, the yard
signs a short statement — this user, this device, this yard, an
expiry — with the yard key the edge's map already holds for the yard's
signed calls, and the device presents the voucher, not a probe answer,
at `POST /user/push/register`; the router checks the signature against
the map's key as it checks a signed call today, and the probe is never
made. Rotation is the phones' existing habit (they register again on
launch, and the relay's recreated store already asks them to), and
revocation is the expiry plus the relay's existing send-time
revalidation of rows against the map. (c) The relay trusts the device
token's hash presented by the device: the yard would have to give the
relay its devices' token hashes, a new yard-to-edge credential flow,
to save a signature the edge can already check. Ranking: (b) first —
it replaces a read with a check the edge already knows how to make,
and it removes the last deliberate read of a device answer rather
than grandfathering it; (a) second, as the slice-boundary fallback;
(c) not taken. What (b) does not hide, it must not claim to: the
voucher's statement — user, device, yard and expiry — is clear
text beside its signature, because the router must read the yard
to check the signature against its map and must name the device
its push rows stand for. A signature authenticates those fields;
it does not conceal them, so registration still tells the relay
which device of which yard is registering, as the register call's
own yard header and the probe's answer already told it. What ends
is the read of an answer the yard composed, and the handing over
of a token to make the router ask for one.

**Device tokens.** Still needed, demoted, and finally hidden. The
channel authenticates a call by the device's static key, which the
yard can check without anything crossing; the token's remaining work
is to name the device record before a channel exists (pairing's own
redeem), to be the lookup a yard does for a caller that has no channel
yet, and to be what `yardr user device rm` revokes in a person's
words. Candidates: (a) keep the token, carried inside the envelope,
checked by the yard as today beneath the channel check — two warrants
where one would do, and a stolen channel key alone still wants the
token file too; (b) keep the token for pairing and for the calls a
yard still answers plainly (its own listener, and the link before
the refusing release), and
let the channel key alone authenticate enveloped calls, with removal
deleting both records in one write and purging the sessions they
founded (section 3.2); (c) abolish the token at pairing
and let the device key be the only credential from the first call.
Ranking: (b) first. It keeps every existing refusal, list and event
meaning what it means (`ResolveDevice`, the `device_tokens` bucket,
last-used and `via <device>` all stand), it makes the edge's theft of
a token worthless without the phone's key file beside it, and it
leaves (c) available later without a flag day. Under any of them the
edge stops seeing the device token at all — its own session
(`X-Edge-Token`, section 3.2) it still reads to resolve the account —
which is the change that disarms
the compromised edge of section 2.5: approval by stolen token ends
because that token no longer crosses, and approval by the channel
needs the device's private key, kept where each app keeps its token
today — footplate's 0600 file beside its settings, the phone stores
their platforms give it — whose strength section 3.8 states plainly.

**Approvals and `GiveSecret`.** Approvals cross today and keep
crossing; what changes is that the edge can neither read the ask a
card carries nor approve one itself (above), while the yard's own
gates — operator's device, `weighed`, the strong card's rules —
stand untouched, because they judge the caller the yard resolved, not
the transport. `GiveSecret` is the one policy the channel puts in
question. Today a connected yard takes only a harness login's code
down the link and refuses every other secret there, for the stated
reason that the edge reads what crosses. Under E2E that reason is
gone for an enveloped call and stands for a plain one. Candidates:
(a) leave the policy exactly as it is — the code crosses, now sealed,
and nothing widens; (b) take every secret from the operator's paired
device where the call is enveloped, as a hosted yard does today, and
keep the refusal word for word on a plain call; (c) take secrets only
where the yard has made E2E a condition (section 3.5). Picked: (b)
for the operator's own device, and no change for any other device
— because the refusal's
own words give its reason ("the edge saw what was sent with it"), and
a design that removes a reason and keeps its refusal owes the reader
another one; the risk it trades against is real and stated in section
3.8 — a stolen, unlocked phone's reach grows from one login code to
every secret ask, as it already is on a hosted yard. The warning
for that reach is written where it is chosen, when the operator's
device is paired (section 3.9), not as a surprise where a secret
is asked. `SourceGrants`
and the telemetry switch stay refused down the link under all three:
their refusal was never only about reading.

**The web view.** Unchanged, and not claimed. Its script arrives over
the edge's origin, so no channel between an app and a yard protects a
browser tab; section 3.6 takes the later candidates. The same holds
for previews on their hosts.

**Edge-side debugging and logs.** The link log keeps its yard, request
id, status-of-the-outer-call, times and outcome, and loses method and
inner path, which it may no longer read; a sealed call that the yard
refuses inside its envelope is `answered` to the edge whatever the
inner status, so the log's `refused by=yard` line for a device call
ends, and the yard's own log — which already takes the frame's id —
becomes the place a refused call is explained. The `X-Edge-Request`
correlation survives whole, since it is the outer frame's id.
Support's hardest loss is content: "show me the call that failed"
becomes a yard-side act, by design, and the notice must not promise
the edge's operator a debugging view the channel removed.

**App versions that do not speak it.** A device app older than the
channel sends plain calls, as today, until it is updated. A yard
that has the channel tells the two apart by the outer path alone
(`/user/e2e` or any other), so no negotiation round trip is added.
What it does with a plain call is question 5's pick, and the pick
is refusal (section 3.5): from the release that carries the
channel, a plain device call down the link is refused with words
that name the fix, and an older app stops working through the edge
until it is updated — it keeps working at the yard's own listener
and on the LAN, where the edge was never the reader. The client
side of the same rule has no fallback either: once a device has
pinned a yard's channel key, at a pairing or by in-place migration,
a failed handshake to that yard is a refusal in the app ("the yard
could not be reached sealed; update or pair again"), never a quiet
plain call under the old token. A silent downgrade would hand a
compromised edge, which can break handshakes at will, the plain
call it could not otherwise read, and would burn the honesty of
every "sealed" word the app had shown. A newer app against a yard
that predates the channel learns there is no channel from the
pairing answer, which names no yard channel key, and refuses the
edge path for that yard the same way, saying the yard needs the
release that carries the channel; its direct paths stand.

### 3.4. Question 4: what a person notices

Candidates here are not mechanisms but experiences, and the ranking is
by how little a person who never asked about encryption is made to
carry.

**Pairing.** With 1B, nothing new to do for the operator's own
pairing: the QR a person already
scans or pastes carries one more field, the code is still eight
characters, and pairing still ends with the device let in and no
second ceremony to perform. A member's pairing is the one place
that changes. Today a member pairs their own app from the web
view's pairing page (`internal/web/pairing.go`,
`internal/web/templates/pairing.html`): they sign in, open a
pairing there, and scan the QR the page shows. That page reaches
them through the edge, which can read or rewrite its code and QR,
so under `require` it can no longer pair an app that calls through
the edge. A member now pairs by a QR the operator opens on the
yard's own screen, which means the operator is present for every
member's new device; the pairing page must tell the member that
pairing from this page no longer gives an app that can call
through the edge, and to ask the yard's operator to open a pairing
QR on the yard's own screen and scan that. That loss is accepted
for now (mckean's pick on ask 20536): no self-service member
ceremony is designed here, and one is a later design. A typed
code with no
QR is the one place a new step can appear — a PAKE run is invisible
(computation, under a second, no words to compare), while 1C's check
is a person reading six digits aloud to themselves. The notice a
person does see is one line of state, not a step: footplate's yards
list already says how each yard is reached, and gains the words for a
channel ("sealed to this yard") beside the yards it can say it of.
On a phone, those words wait for the review of section 3.2 (the
pick on ask 20524): until it passes, the phone shows its device
as not yet sealed, and only then says sealed to this yard.

**Latency and battery.** One Noise handshake when an app opens a yard
(one round trip through the edge, beside the events stream it already
opens), then an AEAD seal and open per call and per stream record —
arithmetic a phone does not feel next to a 30-second call wait and a
network round trip, with no lock making one call wait on another (the
explicit sequences of section 3.2), and no new wakeups: the events
stream, its 30-second beat, now an empty record, and the link's
30-second ping are the cadence that exists, and push still wakes a
closed app as it does today. A design
that re-handshakes per call (the sealed-box shape 3.2 refuses) would
add a round trip to every call and is rejected on this ground too.

**Existing phones, a lost phone, several yards.** Three candidates
for the phones already paired. (a) Re-pair every device: one ceremony
each, the full guarantee from 1B, and a week of stragglers locked out
of any yard that has begun refusing plain calls. (b) Migrate in
place under 1D: on its first start after the update, each app makes
its key pair and registers the public half in a call the existing
token authenticates, the yard pins it and answers its own key signed
by its identity, and the yard records the device as sealed-from that
day; an edge that rewrote that one call holds that device's channel,
which is 1D's failure accepted for a migration and disclosed in the
device list's words. Stated without softening, because the later
words depend on it: an edge that is active at that one migration
call — substituting its own key for the device's, or its own for the
yard's signed answer — can read every call that device makes from
then on, undetected, for as long as the device stays on the keys
that call pinned; it is not limited to delaying or reading the
migration call itself. An edge that only logs cannot do this: it
holds the token and the true keys that crossed, and loses the
channel from the first sealed call. The risk therefore stands for
every device recorded `sealed (in place)` until that device is
re-paired by QR (1B). The handling is picked (section 5, Q4): the
device keeps working as `sealed (in place)`, that label is lasting
for as long as the device stays on the keys its migration pinned,
the app offers re-pairing by QR beside it as the way to lift it,
and re-pairing is never required; the public claim is qualified
the same way (sections 3.8 and 3.9). No sentence in this document,
the notice or the summary may describe an in-place device with the
unqualified words meant for a device paired by QR. That registration is the one plain device call
the refusal of section 3.5 still carries after the channel release:
one call per pre-release device, authenticated by the token it
already holds, readable by the edge that once, and refused for any
device that already pins a key, so it cannot be replayed as a
downgrade path. (c) Migrate at next pairing only, plainly, and
let old pairings age out by the inactivity habits the apps already
have. Picked: (b), with its record kept and its handling picked
with it — a yard can say which devices were pinned at a ceremony
and which in place; an in-place device keeps working under its
lasting `sealed (in place)` label, the app offers re-pairing by QR
beside that label as the way to the ceremony's guarantee, and no
one is required to take it; (a) is (b)
with the choice made for everyone at once, and (c) leaves the long
tail plain by default. A lost or reinstalled phone is exactly today's
story with one more file to lose: `yardr user device rm` deletes the
token's hash and the pinned key together and ends the device's live
channel sessions and streams in that moment (section 3.2), and a
reinstall pairs again,
and nothing of the old channel can be recovered from the yard, which
never held a private half. Several yards per account stay several
pairings: each yard's channel key is its own, each device key is made
per yard and kept beside that yard's token file, so yards share no
key material and the edge cannot join a device's yards by a key it
never sees — it joins them by the account session it already checks,
which E2E does not hide and does not claim to.

### 3.5. Question 5: optional, for whom, and what the notice may say

mckean's second question — could it be optional — has three scopes,
and the scope decides what claim the public words may carry.

**5A. Per device.** A pairing is sealed or plain as it was made, and
the yard's device list says which. Cost: the least machinery (a field
on the device record, beside the token's hash), and every yard carries
mixed traffic for ever, so the edge's notice must always keep the
plain sentence for someone; a person chooses per phone, usually once,
usually by which app version they have rather than by judgement.
Fails as a claim: "the edge cannot read your device's calls" is true
only with a footnote naming the devices it is false of.

**5B. Per yard, at `warn` first.** A yard setting says `allow`,
`warn` or `require`, judged on calls that arrive down the link, and
only there: the same device on the yard's own listener or LAN is
the yard's local affair, where the transport's reading was never
the edge's. Cost: one setting, one refusal, and a yard-wide day on
which plain device calls through the edge end; every device of
that yard must be sealed, migrated (3.4b) or re-paired by then, and
the operator is the person who hears about it. Its kind form,
`warn`, lets plain calls through with a line in the device list
while phones migrate, and pays for that kindness in the notice:
while any yard warns, the public sentence needs its footnote.

**5C. Always, for every connected yard.** The protocol has one shape;
a plain device call down a link is refused from the release that
carries the channel. Cost: no setting, no mixed state, the shortest
notice — and a flag day across every repository the channel
touches and every installed
phone at once, with app-store review times in the middle of it, and
no retreat for the yard whose one old phone is its operator's only
door. Fails operationally before it fails cryptographically, unless
the refusing release lands only after every client that speaks the
channel has shipped (bead 9's place in the order, section 6).

**Picked, question 5: 5C's rule, landed in 5B's order — `require`
from the first release that refuses, with no `warn` phase.** From
the release in which the yard refuses, a device call that reaches
a connected yard down its edge link unsealed is refused, with words
that name the fix (update the app, or answer at the yard); there is
no yard setting to soften it, so no yard carries mixed traffic and
no phone needs to learn a per-yard rule. `require` also ends the
member's self-service pairing described in sections 3.1 and 3.4:
a member can no longer pair an app that calls through the edge
from the web view's pairing page (`internal/web/pairing.go`,
`internal/web/templates/pairing.html`), because the edge serves
that page and can read or rewrite the code and QR it shows; the
member pairs by a QR the operator opens on the yard's own screen,
and the page must tell the member so, in those words. That loss
is accepted for now (mckean's pick on ask 20536): the member
pairs by the operator's QR, and a self-service member ceremony
is a later design this document does not give. Refused is not unread.
TLS ends at Cloudflare and at the router, and the router reads a
request's method, path, headers and body before it frames the call
for the link (`yardr-hosted/router/internal/router/router.go`, the
`serveDeviceCall` read ahead of its forwarding), so a plain attempt
by a client too old to seal has already been sent in the clear to
Cloudflare and to the edge before the yard refuses it: the yard
does not carry it out and returns no answer beyond the refusal, but
the edge could read the attempt as it arrived. What limits that
exposure is the client rule of section 3.3: once a device has
pinned a yard's channel key it never sends a plain call to that
yard again, and a failed handshake is a refusal in the app, never a
fallback to plain; the only senders left are clients that were
never updated, and the refusal itself tells them to update. Two
narrow exceptions are
not plain device calls and are named wherever the claim is printed:
the pairing redeem, which new pairings run inside the handshake
itself, and the one-time in-place migration registration of a
pre-release device (section 3.4), refused once a key is pinned.
The order is what makes the rule survivable: the yard learns to
open envelopes first, the desktop and phone clients that speak
them ship next, and the refusal lands last (bead 9) — and only
once those clients' releases are available to users on each
platform (desktop, iOS and Android), not merely merged, which
with the phone stores not yet live holds bead 9 back until they
are — so the app a
person already carries fails only if it was never updated after
its sealed successor was available to install — the rough month 5C names,
taken deliberately rather than by accident. A client older than
those releases loses edge access when bead 9 lands. With no `warn` state,
the notice and yardr.dev's words can carry the unqualified
sentence for sealed calls from that release — "device calls cross
the edge sealed; the edge cannot read them" — beside the web view's
and previews' qualification in the same type size (question 5 of
section 5), and beside a plainer sentence the refusal requires:
that an attempt sent plain by an app too old to seal is refused
without being carried out, but was readable as it was sent, so no
wording may claim a refused attempt was unread. That distinction
dies with the last unupdated client; the sealed sentence does not
wait for it. 5A survives only as the state a migration passes
through on its
one registration call, named as such, not as a scope anyone picks.

### 3.6. Question 6: what later, and what not

**Peers through the edge: out of this design's guarantee, for a
later design.** Two yards already hold ed25519 identity keys and
already authenticate every request and answer by signature, but
that is not a channel key. Bead 1 adds an X25519 channel key for
device pairing; nothing in this design says how two yards would
receive and verify each other's X25519 key, what happens to peer
records that already stand, or how such a key would be rotated.
Until a later design answers those three — key distribution and
verification between yards, existing peer records, and rotation
— peer calls through the edge stay as readable to the edge as
they are today, and no bead here seals them. The device envelope
may be the shape that later design reuses, but reusing it is not
designed or promised here.

**The web view: not by this mechanism, not soon.** A browser's trust
anchor at `my.yardr.dev` is the edge's certificate; sealing payloads
to a key the yard pins would ask the edge's own served script to keep
the secret from the edge that served it. The honest candidates are
outside this bead: signed web assets checked by a browser extension
or an installed app shell (a new trusted program on the person's
machine), or accepting that the view is the operator's window and
keeping its present defences (owner alone, one-minute minted tokens,
fresh sign-in for strong cards). Name it plainly in the notice as the
surface E2E does not cover, and let no summary sentence imply it does.

**Hosted yards: not worth claiming.** The yard, its key file and the
edge all stand on the service's side of the line; a device channel
would hide calls from the router process while the yard beside it
holds the keys that open them. If a hosted yard one day runs in a
confidential-computing boundary the service cannot read, the same
envelope serves unchanged — the mechanism is ready, the claim waits
for its fact. **Previews** share the web view's anchor problem on
their own hosts and take the same answer. **Webhooks** cannot: their
senders will not encrypt to a yard, and the yard's own verification
with secrets the edge never holds is already the right shape there.

### 3.7. Question 7: order, size, and the smallest first slice

Six repositories gain code or words, one of them mostly a door left
open: `yardr` (the yard's channel key, pairing, envelope, refusals,
voucher, device record), `yardr-hosted`'s router (one outer path
passed on, the voucher checked where the probe stood, the link log's
lost fields), the three device codebases — `footplate` (the desktop
window, Rust), `footplate-ios` and `footplate-android` — each gaining
a key pair per yard, a handshake, and one envelope where it builds
calls today, and `yardr.dev` (the site's edge words, bead 14 alone).
The order is the order of section 6's beads: yard first
(a yard that opens envelopes no device sends yet changes nothing),
router beside it (a path it passes without reading is a small step
beside the routes it has), desktop footplate as the proving client
(one codebase, no app-store clock), the voucher where the probe
stood — its yard side first (bead 5, `yardr` alone: issuance), its
router side after (bead 6, `yardr-hosted` alone: validation, depending
on bead 5) — then the two phone apps with migration, then the refusal
of plain calls (bead 9) and, one bead per repository, the notice's
and the documents' new words (beads 10 to 14), and the PAKE for
typed and read-aloud codes last, one bead per repository that
needs it (beads 16 to 19, the yard's half first). Peers are not in
this order: bead 15 defers them to a later design and builds
nothing (section 3.6). The
smallest first slice that is honestly end to end — yard, router
door, one client, QR pairing, sealed calls and stream,
the channel key in the token's place through the edge — is beads 1
to 4 of section 6; through it a yard still answers plain calls, so
no summary written before bead 9 may claim the edge reads no device
call, and push keeps
working through it on candidate 3.3(a)'s named exception until bead 6
replaces the probe, and no summary written before bead 6 may drop
that line. The whole programme as scoped here is nineteen
headings (section 6), eighteen of them build beads: `yardr`
carries beads 1, 2, 5, 9, 11, 12 and 16, `yardr-hosted` beads 3, 6
and 10, the desktop `footplate` beads 4, 13 and 17,
`footplate-ios` beads 7 and 18, `footplate-android` beads 8 and
19, and `yardr.dev` its words in bead 14. Bead 15 is the peer
deferral (section 3.6): it belongs to no repository and builds
nothing. No bead crosses a repository: bead 5 issues the voucher
in `yardr` and bead 6 checks it in `yardr-hosted`, the second
depending on the first, bead 16 is the PAKE's yard half in
`yardr` and beads 17 to 19 are its client halves, each in its
own repository and each depending on bead 16, as the notice and
document beads already land one repository at a time.

### 3.8. The threat model

Three readers, in ascending strength, and what each can and cannot do
under the picks (1B with 1A behind it, 2B, `require` from the first
refusing release). A fourth party
— someone who holds the yard's machine or the phone itself — is
outside: the yard and the device are the ends, and an end read at home
is not the channel's failure, on any candidate.

| Who | Can still do | Cannot do any more |
|---|---|---|
| The edge's operator, reading at leisure (logs, queue files, a lawful demand, a curious afternoon) | Read the edge session (`X-Edge-Token`) on every enveloped call, as today — it stays outside the envelope so the router can resolve the account (section 3.2); see that an account's yard had sealed calls, when, how long, how large, from where; read webhooks, the web view and previews, and peer bodies (peers have no slice in this programme; section 3.6 defers them to a later design); keep the push tokens the relay must hold; read the one plain probe until bead 6, and each pre-release device's one plain migration registration (its token and the key it registers, section 3.4); read the voucher's clear fields — user, device, yard, expiry — at push registration; read a plain attempt by a client too old to seal — its method, path, headers and body as sent — because the router reads the request before the yard refuses it (section 3.5); read the member pairing code the web view's pairing page shows, which reaches the member through the edge (sections 3.1 and 3.9) | Read a sealed call's method, path, headers, body or answer; find a device token, a pairing code or a harness login code in anything a sealed call carried; approve an ask later with what was logged. A refused plain attempt is not carried out and earns no answer beyond the refusal, and a device that has pinned a yard's key never sends one — but the attempt itself was readable as sent, and no claim may say it was not |
| A compromised edge, active (rewriting, replaying, refusing, forging its own headers) | Refuse, drop, delay or reorder service (a record delayed past the replay window fails its call whole, section 3.2); mint correctly signed outer frames, since it holds the signing key, and submit invalid envelopes, which fail their AEAD opening as failed calls — failed opens and denial of service at the cost of the yard's openings (section 3.2); measure timing and size; spend its one online guess per PAKE run or five plain guesses per pairing, as a stranger could; assert a presenter at pairing, narrowed by the code as today; serve a web view or preview script of its own devising; hold a record sealed before a device's removal and deliver it after — the device will open it, within the limit section 3.2 states | Open a sealed call or answer, recorded now or later (a session's ephemeral keys see to later); put itself in a pairing held by 1B or 1A undetected; replay a sealed message inside its session, move one between calls or streams, or revive one after its call has closed (call-bound associated data, one opened-sequence window for the session, and closed-call marks, section 3.2), or forge a valid sealed record — the AEAD needs the session keys it never learns, so an envelope it signs correctly still fails its opening, while an outsider's forged frame and a frame replayed on a later link or out of order are refused earlier, by the signature and its session and sequence (section 3.2); talk a pinned device down to a plain call by breaking its handshakes (a pinned device refuses plain and fails the call instead, section 3.3); keep a removed device calling on a session it founded before the removal (the first record of each call re-reads the device record, and removal purges its sessions and cancels their streams, section 3.2); approve an ask, give a secret, or read a screen with anything it holds — no device token crosses in a sealed call (the edge session it reads is its own, section 3.2), and the channel key never leaves the phone |
| Cloudflare, ending TLS in front of the router | Everything the first row can, over the traffic at its termination point, plain attempts and the edge session (`X-Edge-Token`) included; refuse, drop, delay, reorder or alter the outer traffic it carries, as the compromised edge can; forge, drop or replay a zero-length beat record, keeping a stream seemingly alive or letting it be cut | Read, forge or undetectably alter a sealed call or any inner byte: what it terminates TLS onto is an envelope, not a call, and any alteration of a sealed record or of an associated-data-bound field (the tag, direction, call id, sequence, index, and, on a call's first record, the outer path and the stream marker, section 3.2) fails that record's opening and becomes a failed call, never a call silently read or changed. The unauthenticated controls give no such promise and need none: beat records, frame kind, lengths and timing altered, forged or dropped touch availability only — a stream may end or linger, a call may fail whole — and no inner byte is read or altered by them, because every inner byte crosses only inside a record that must still open. The channel gives integrity for records and inner content, not availability: refusal, dropping and delay stay open to it, as they do to the edge |

Two residues are stated so no summary may round them away. Metadata
is not hidden: the account, the yard, the fact and rhythm of calls,
their sizes and the device's network address are the routing's own
data, visible to the edge and to Cloudflare under every candidate
short of padding and cover traffic, which no candidate proposes. And
the phone's key is only as strong as its keeping: an app that keeps
its channel private key beside its token in a 0600 file is protected
against the network and not against the phone's own compromise, and
where a platform gives the app a hardware-backed store, using it is
that client's bead, not this design's assumption. Under 1D alone
(migration before re-pairing) the compromised edge's row gains one
line, stated here as it is in section 3.4 so no later sentence may
round it away: an edge active at a device's one in-place migration
call, substituting its own key for the device's or for the yard's,
can read every call that device makes from then on, undetected,
until the device is re-paired by QR; an edge that only logged that
call cannot. The device list's `sealed (in place)` against `sealed
(paired)` is the record of that difference: picked to last until
the device is re-paired, with re-pairing by QR offered in the app
and never required, and the public claim qualified the same way.

### 3.9. What a person would type and see

Pairing a phone to a connected yard, after the pick. The yard's half
is the command that exists, with one line longer:

```text
$ yardr user device pair mckean phone --qr
Pairing is open for 10 minutes, until 14:32:10, for the device phone of mckean.
Give the device this yard's address and the code:

  address  https://my.yardr.dev
  yard     mckean/airy
  code     K7QM-2XPD

  <the QR code, now holding the yard's key beside the code>

The code is good once: the device is answered inside a sealed channel,
and the edge that carries it can read neither the code nor the device token.
It then calls as mckean, sealed to this yard, until: yardr user device rm mckean phone
This is your own device: sealed, it may give the yard any secret a
setup asks for — protect the phone as you would this machine.
```

The phone scans, and says what it has always said, with its state
named in its yards list from then on: `airy at my.yardr.dev, as its
device phone, sealed to this yard`. A phone that migrated in place
says instead `sealed (in place)` there, with the lasting line of
section 3.4 beside it — re-pairing by QR gives full protection —
and an offer to pair again by QR that it need never take; the label
stays until it does. Nothing else is typed, compared
or confirmed — a pairing for any other person carries no such
warning, since no other device gains the widened reach. At the
yard, the standing words gain one state per device and no switch:

```text
$ yardr user device list
mckean  phone   added 11-05 09:12, last used 11-05 09:40 from the edge, sealed (paired)
mckean  tablet  added 10-28 18:03, last used 11-04 22:15 from the edge, sealed (in place)
```

And the refusal a plain device meets at such a yard, in the words the
link already carries refusals in, names its fix: the call is refused
unsealed, update the app or pair the device again, and nothing was
carried out or answered. The words may not add "or read": the
attempt was sent plain, so Cloudflare and the edge could read it as
it arrived (section 3.5); what the yard promises is only that it
did not act on it. The edge's notice, rewritten once the refusal
has landed (beads 10 and 11), trades its device sentences for the
sealed claim, its refusal sentence and its qualifications in the
same type size:
device calls cross the edge sealed and the edge cannot read them —
calls, answers, device tokens, codes, screens. "Tokens" there means
the device token, which no sealed call carries (section 3.2); the
edge session (`X-Edge-Token`) the edge issued to the account is still
read by the edge to route, as today, and the claim does not hide it.
"Codes" there means the
pairing code redeemed inside a sealed handshake and the secret
values a sealed call carries; it does not mean every code: the
edge still reads the member pairing code the web view's pairing
page shows (`internal/web/pairing.go`), because that page reaches
the member through the edge, which is why that page no longer
pairs an app for calls through the edge (sections 3.1 and 3.4).
An attempt sent plain by an
app too old to seal is refused without being carried out, but was
readable as it was sent — update the app; a device sealed in place
carries, until it is re-paired by QR, the one narrower promise of
section 3.4 in the same size — an edge that cheated at its
migration call could read its later calls undetected, where a
device paired by QR carries the full one, and the app offers that
re-pairing without requiring it — and, in letters no smaller, the
web view and previews are still carried readable, as are webhooks
and the fact and size of every sealed call.

### 3.10. How comparable relays do it

Each of these was read from its own documentation on 5 November 2026;
the last section holds the links. They divide cleanly into relays that
cannot read what they carry, and tunnels that can.

**Signal's linked devices.** A new linked device generates a key pair
and a temporary provisioning address, puts the address and its public
key in a QR, and the primary device scans it and answers with a
provisioning message encrypted to that key — the out-of-band key of
candidate 1B, run in the opposite direction: Signal's new device
shows its key to the account's authority, where yardr's yard, the
authority, shows its own to the new device. History then moves
device to device as an encrypted bundle through the servers, which
relay ciphertext throughout. The lesson yardr takes: the QR-carried
public key, not a password, is what a consumer pairing converged on,
and the relay's blindness is the premise the whole account model is
built on, not a mode.

**WhatsApp's linked (companion) devices.** Each companion has its own
identity key; the primary device signs the list of its companions, and
clients check that signed device list before encrypting to a device —
client fan-out, one encryption per device (its *Encryption
Overview*). The lesson: per-device keys plus a signed, revocable
device list is the same shape as yardr's device record with a pinned
key beside the token's hash, and removal from the list is the revoke.

**Tailscale's DERP.** A relay of last resort that "blindly forwards
already-encrypted traffic", in Tailscale's own words, because private
keys never leave the device that generated them; the coordination
server introduces, the relay carries WireGuard ciphertext. The lesson
is candidate 2C stated by its owner: where the unit of traffic is
already an encrypted packet, blindness is free. Yardr's unit is a
signed HTTP frame, so yardr buys the same property with an envelope
(2B) instead of a second pipe.

**Matrix.** Homeservers store and forward events whose content is
encrypted per room (Olm for device pairs, Megolm for groups), and a
device is trusted by verification — an interactive comparison (emoji
or a scanned QR) that cross-signs it — with current specification
work excluding devices nobody cross-signed. The lesson runs both
ways: encryption to named devices with verification the person
performs is proven at scale, and its cost is exactly candidate 1C's —
a verification burden users feel, and a long tail of unverified
devices a strict yard must one day refuse (3.5's `require`).

**magic-wormhole.** A mailbox server relays the opening messages of a
SPAKE2 PAKE run over a short human code; the derived key encrypts
everything after, the code is one-time, and a malicious mailbox gets
one guess. It exists, by its own account, to make PAKE usable. The
lesson is candidate 1A's evidence: where a short code is the one
secret two ends share, a PAKE is the settled primitive, and its cost
is the implementation, not the ceremony.

**Syncthing's relays.** A relay joins two devices that then speak
their usual TLS inside the joined byte stream, so the traffic "is
completely meaningless to the relay", which learns device identities
and session facts only. The contrast with yardr's peers (3.6) is
the point: Syncthing's ends already hold the keys their TLS needs,
while two yards hold identity keys but no channel key, so the relay
question for peers does not dissolve here — how they would come to
hold a shared channel key is the later design bead 15 defers.

**ngrok and Cloudflare Tunnel (not end to end).** ngrok's HTTPS
endpoints terminate TLS at its cloud service and re-encrypt into the
agent's tunnel by default; its end-to-end shapes are the explicit TLS
and TCP configurations where the cloud forwards without terminating.
Cloudflare Tunnel likewise ends the visitor's TLS at Cloudflare and
carries the request onward. The lesson is the negative control: a
tunnel is a reading relay with good manners, and any yardr sentence
that calls today's edge path encrypted without naming who holds the
keys repeats their elision. Home Assistant Cloud's remote UI is the
counter-example that proves a reading relay is a choice: its proxy
routes on the TLS handshake's SNI and forwards sealed bytes to the
instance, whose own Let's Encrypt certificate key is generated and
kept on the instance — the web view's problem (3.6) solved there
because the browser's anchor is the instance's own certificate,
served under the instance's own name, not a page drawn at one shared
origin; that the service controls the names and their issuance is
the residual trust its own account leaves with the person.

## 4. Left out

Building any of it, by the bead's own scope. Changing `docs/edge.md`
or the edge's notice before a pick: both are rewritten by the beads
that make their new sentences true (section 6), and a notice edited
ahead of its fact is worse than a plain one. Webhooks, for the reason
section 2.6 gives. The web view and previews, beyond naming them
uncovered in every claim (sections 3.6, 5): any honest candidate
needs a trusted program on the person's machine, which is a product
decision about extensions and app shells, not a channel decision.
Hosted yards, until a fact about their boundary changes (3.6).
A self-service member pairing ceremony whose secret never crosses
the edge: that loss is accepted for now (ask 20536), members pair
by the operator-opened QR, and the ceremony is a later design.
Metadata hiding — padding, cover traffic, sealed yard names: the
routing must name its yard, and disguising rhythm is a research
project wearing a relay's clothes. Post-quantum key agreement, a
second device-to-device channel between a person's own devices
(Signal's history transfer has no yard analogue: a new phone's past
is the yard's present, fetched by call), and recovery of a channel
key from anything but a new pairing: a lost key is a removed device,
as a lost token is today. Key transparency or any public log of yard
keys: the QR is the distribution, and a yard's key change is handled
as a re-pairing, not published. Finally, encrypting the LAN path: a
device calling the yard's own listener was never the edge's to read,
and yardr's standing rule that TLS there is the operator's proxy to
supply stands untouched.

## 5. The questions, as decided

mckean decided each of these on 5 November 2026, answering the
bead's asks, the last two the same day: Q7's phone review rule —
build the phone beads now on the bound library, and gate the
phones' sealed claim on an outside review of the bound version,
the binding and the framing (ask 20524) — and the loss of member
self-service pairing from the web view, accepted for now, with
members pairing by the operator-opened QR and a self-service
ceremony left to a later design (ask 20536). Each entry gives
the options that stood, what
each costs, and the pick that sections 3 and 6 now build on. The
decide stage's remaining work is to confirm the document says
what was picked, not to pick any of it again.

**Q1. The founding ceremony — picked: 1B now, 1A next.** Options:
the QR-pinned yard key alone (1B), with typed codes staying plain;
1B plus a PAKE for typed codes whose code never crossed the edge
(1A, four beads more — beads 16 to 19, one per repository — a
reviewed PAKE library in four codebases); or
the compared check (1C) everywhere, reusing peer pairing. The PAKE
covers the typed and read-aloud shapes whose code the edge never
saw; it does not cover member pairings opened through the app,
whose code the router itself reads and returns (section 3.1), so a
member's sealed claim stays 1B's QR from the yard's screen, and an
edge-independent member ceremony is a later bead only. 1C costs a
person's attention on every pairing; 1B alone leaves the read-aloud
code as readable as today's. The pick ships 1B first at no extra
ceremony, and 1A as the decided next slice (beads 16 to 19); 1C is not
shipped unless a PAKE proves unobtainable.

**Q2. The plain-call rule — picked: `require` from the first
release that refuses, no `warn` phase.** Options: `warn` from the
first release and `require` as a floor a season later; `require`
from the first release; or per-device choice with no yard word
(5A). `require` first is the cleanest claim and the roughest month;
per-device is the kindest and never finishes; `warn` is a promise
to finish later that someone must remember to keep. The pick takes
the rough month, softened only by order: the refusal (bead 9) lands
after every sealed client's release is available to users on its
platform — desktop, iOS and Android, not merely merged, and held
back while the phone stores are not yet live — and a phone that still
speaks plain is refused through the edge until it updates. A
client older than those releases loses edge access when bead 9
lands.

**Q3. Secrets over a sealed link — picked: every secret from the
operator's own device where the call is sealed; no change for any
other device.** Options: no widening (only the harness login code
crosses, as today); every secret from the operator's device where
the call is sealed (3.3b); widening only where a yard also requires
the channel. The first keeps a refusal whose reason the channel
removed; the second grows a stolen phone's reach to what it is on
a hosted yard; the third ties a person's convenience to their
strictness. The pick takes the second, with its warning written
where the operator's device is paired, not where a secret is
asked.

**Q4. In-place migration — picked: migrate in place, warn
lastingly, and offer QR re-pairing.** Options: re-pair every device
(3.4a), migrate in place with the distinction recorded (3.4b), or
migrate at next pairing (3.4c); and, for the handling of the
distinction, keep the devices working under a lasting `sealed (in
place)` warning, label them not sealed until re-paired, or drop
in-place migration. The pick takes in-place migration with the
first handling: an in-place device keeps working as `sealed (in
place)`, the label and its line — an edge that cheated at the
migration call can read its later calls, re-pairing by QR gives
full protection — last until the device is re-paired, the app
offers that re-pairing and never requires it, and the public
claim is qualified the same way, because the difference is both a
fact about each device's past that only the record remembers and
a live limit on its promise until it is re-paired.

**Q5. The web view's sentence — picked: claim the apps sealed, and
name the web view and previews edge-readable in the same type
size.** Options: that sentence, indefinitely; or hold all E2E
claims until a trusted browser-side program exists. The first is
true and printable from the refusing release; the second makes
the perfect the enemy of a real improvement to the surface people
actually carry in a pocket. The pick prints the first.

**Q6. The Noise implementation — picked: a maintained library in
each language, named in section 3.2, and no Noise code of our
own.** Options: a maintained library per language; or our own
constructions on `x/crypto`-style primitives, held by shared test
vectors in Go, Rust, Swift and Kotlin. Writing the constructions
ourselves is the riskiest code in the plan, and `x/crypto` carries
no Noise package to build on; the pick takes the maintained
libraries, each vetted against the specification's vectors and
against the others in its own bead — with the phones narrowed by
Q7 below.

**Q7. The phone library's review rule — picked: find an audited
library first; the phones build now on the bound library, and
their sealed claim waits for the review.** Options: gate the
phones on an outside review of
Torann (or a switch to an audited library), with phones on plain
TLS until then; ship Torann on its interop checks as a preview and
review later; or weigh audited libraries for iOS and Android
before phone work starts, bridging an audited Rust or Go
implementation into the apps where no native one qualifies. The
first pays money or weeks for the library already weighed; the
second rests the first sealed phones on unaudited, not-production-
ready code; the third swaps the library now, while swapping is
cheap. The pick takes the third, as section 3.2 weighs it: no
native Swift or Kotlin Noise library with an independent audit
and a production-ready statement was found, `snow` carries its
own no-formal-audit warning, and the phones therefore bridge the
yard's `flynn/noise` by `gomobile` as the mechanism, Torann
remaining only as a fallback marked unaudited.
That bridge does not yet meet the rule it was picked under.
Cure53's Turbo Tunnel review was a white-box review of `dnstt`
with `flynn/noise` as used there (UCB-02-003, nonce overflow;
UCB-02-004, the deprecated Curve25519 API; fixed in v1.0.0); it
covers neither the pinned v1.1.0, which adds
`UnsafeNewCipherState`, nor the `gomobile` binding, nor this
design's explicit-nonce and replay framing. The version and the
binding are therefore not yet reviewed, and this design does
not call them audited. What unlocks the phones' sealed claim is
an independent review of `flynn/noise` v1.1.0 as bound by
`gomobile` and of that framing, named as its own later step.
On the standing choice mckean picked build now and gate the
claim (ask 20524): beads 7 and 8 build now on the bound v1.1.0,
no phone shows a sealed claim until that review is done — a
phone shows its device as not yet sealed until then, and no
public text claims more — and phone work still never presents
unaudited code as audited.

## 6. The beads it would become

Nineteen headings, in the order they would land — eighteen build
beads and, at bead 15, the peer deferral, which builds nothing.
Each build bead is small enough that
a builder finishes it without asking; the dependencies say which wait
for which. Repositories are named per bead; every bead's tests are
its own repository's gate, and any bead that starts a server or an
agent does it in its depot's isolated harness, never the operator's.

1. **A yard channel key, and pairing that hands it over sealed.**
   Goal: the yard holds one X25519 channel static beside its ed25519
   identity, signed by it; `yardr user device pair --qr` carries the
   channel public key as the QR's fourth field; the pairing redeem
   runs as the first message of a Noise handshake to that key, run
   by `github.com/flynn/noise` (section 3.2), and
   the answer (token, user, device, yard) is sealed in reply.
   Where to look: `internal/yardkey/yardkey.go`,
   `internal/store/devicepair.go`, `internal/store/devices.go`,
   `internal/web/pairing.go`, `internal/web/templates/pairing.html`,
   `docs/users.md` ("Enrolled by a code"). Done when: a device that
   pairs by the new QR is recorded with its pinned public key beside
   its token's hash, a pairing with no key field behaves exactly as
   today, a redeem transcript recorded and replayed buys nothing,
   and the web view's pairing page no longer presents its QR as a
   way to pair an app that calls through the edge: it tells the
   member that pairing from this page no longer gives such an app,
   and to ask the yard's operator to open a pairing QR on the
   yard's own screen and scan that (sections 3.1 and 3.4; that
   loss is accepted for now, ask 20536).
2. **The yard opens envelopes where its link hands it calls.** Goal:
   an outer `/user/e2e` call down the link is opened after the frame
   check, served as the inner call its envelope carries, and its
   answer — whole or streamed, the events stream included — is sealed
   record by record under the explicit sequences and per-call indexes
   of section 3.2 and closed by its sealed final record, each
   message opened with its own copied cipher state in its serving
   goroutine and bound by its associated data to the call id its
   frame registered before that goroutine started; the channel key,
   not the token, authenticates an enveloped call, each new call's
   first record re-reads the device record its session names, and
   removal deletes both records, purges the device's session entries
   and cancels its live calls in the same server process. Where to
   look: `internal/edgelink/link.go`, `internal/rpc/user.go`,
   `internal/rpc/api.go`, `internal/store/devices.go`,
   `internal/server/edge.go`. Depends on
   bead 1. Done when: a stand-in
   device and a real yard exchange a call, a call whose body needs
   several records, a streamed blob and an events stream sealed end
   to end; 64 enveloped calls and streams in flight at once on one
   session all open, though they arrive and are served out of
   sealing order; a record replayed whole, a record
   with its sequence rewritten, and a message older than the replay
   window are each refused and mark nothing for the honest message
   that follows; a valid record of one of two concurrent streams,
   delivered to the other before its own stream has opened it, is
   refused there and still opens on its own stream, and the same
   record replayed after its source stream has closed is refused,
   neither refusal corrupting the other call; a call made on a
   session established before its device was removed, and the next
   record of a stream live at that removal, are each refused, the
   device's next handshake is refused, and a second device's session
   is untouched; a valid record withheld past the 65,536-record
   replay window is refused and its call fails whole, and the same
   call issued again under a new call id opens; a stream record
   dropped in the middle is refused at the index gap, with nothing
   past the gap delivered as whole, and an outer stream that ends
   without its sealed final record is reported truncated, a part
   blob never saved as a whole one; a first record whose outer path
   or stream marker was rewritten between the device and the yard
   fails its opening and its call, and a forged or dropped
   zero-length beat record alters or reveals no inner byte (a stream
   so treated may end or linger, and nothing more); an inner
   answer's status and headers are nowhere in the outer head the
   link carries back, which shows only the envelope's own status;
   a marked call whose opened inner call is any other than
   `GET /user/events` is refused (checked after the envelope is
   opened, section 3.2), a marked call counts against the
   per-credential stream cap (`StreamsPerCredential`), and the
   seventeenth marked stream of one credential is refused as the
   seventeenth plain one is; and the yard's socket API and LAN
   behaviour are byte-for-byte what they were.
3. **The router carries one path it does not read.** Goal: the
   router routes `/user/e2e` to the named yard's link like any
   `/user/*` call — session checked, bounds on sealed lengths, outer
   status and outcome logged — reads the `X-Edge-E2E-Stream:
   long-lived` marker, drops it as a device header and copies its
   presence and value into the link frame's header beside the call
   id for the yard's associated-data check, copies the
   device's `X-Edge-E2E-Call` header into the link frame's header
   unchanged for the yard's call check, opening a marked
   call under its events rules and an unmarked one under its call
   rules, carries every streamed enveloped answer as length-prefixed
   records, and writes a zero-length beat record at a record boundary
   after one events beat of silence on a marked stream; its link log
   no longer claims a method or path for a call it could not read.
   Where to look: yardr-hosted router README ("Device calls and the
   events stream", "What the edge logs of a link request") and its
   router. Depends on bead 2's shape, lands before any client ships.
   Done when: the router's proof world passes a sealed call, a sealed
   stream and a cut stream; a sealed events stream held quiet past
   one call wait and past Cloudflare's 125-second silence limit is
   still open, having carried only beat records, while an unmarked
   streamed answer silent for one call wait is still cut; a beat
   never lands inside a record, and no beat is sent to the yard; the
   frame a sealed call rides carries the device's call header as the
   device sent it, and the marker's presence and value beside it for
   the yard's associated-data check, with no device stream header
   carried further; a beat forged, dropped or replayed on the
   device's leg changes no sealed record's opening; the yard at
   the link's other end (bead 2) refuses a marked call whose opened
   inner call is any other than `GET /user/events` and counts
   marked calls against its per-credential stream cap
   (`StreamsPerCredential`), so a device that marks an ordinary
   sealed call in the router's proof world gains no long-lived
   slot from the marker the router acted on; and
   its logs show no inner name and no stream but the outer marker's
   class.
4. **footplate (desktop) speaks the channel.** Goal: per-yard key
   pair beside the token file it keeps today, QR and pasted-text
   pairing by the new field, a handshake at a yard's opening (by
   `snow`, section 3.2), every
   call minted a call id and sent under it as section 3.2 names,
   every call and the events stream enveloped with its answer records
   checked against that call, a session the yard reports gone
   discarded for a fresh pairing, and the yards list saying
   `sealed to this yard`. Where to look: footplate's `docs/yards.md`
   and the call-building code it names. Depends on beads 1 to 3.
   Done when: beads 1 to 4 stand as the smallest first slice — a
   person pairs the desktop app to a connected yard through the real
   edge path and no party between opens a call — proved in an
   isolated yard with a stand-in edge, never the operator's; and a
   broken handshake to a yard whose key the app has pinned ends in
   its refusal words, with no plain call sent.
5. **The yard issues a push registration voucher (yardr).** Goal:
   in the `yardr` repository alone, the yard issues a short-lived
   registration voucher — this user, this device, this yard, an
   expiry, signed with the yard key the edge's map already holds —
   at pairing and afterwards on request inside the channel, so a
   device can ask for a fresh one without a probe. No router change
   and no device app change in this bead: how the router checks a
   voucher is bead 6, and which app presents one, and how it asks
   the yard for one, is each phone bead's own work below. Where to
   look: bead 1's pairing answer, `internal/yardkey/yardkey.go`,
   `internal/store/devices.go`. Depends on bead 1. Done when: the
   yard issues a voucher at pairing and on a sealed request that
   verifies against the yard's public key in the edge's map, an
   expired or wrongly signed voucher verifies nowhere, and the
   voucher's fields (user, device, yard, expiry) are its stated,
   signed-not-concealed statement, named as such wherever it is
   described.
6. **The router checks the voucher where the probe stood
   (yardr-hosted).** Goal: in the `yardr-hosted` repository alone,
   `POST /user/push/register` and `unregister` accept a yard-signed
   voucher in place of a device token; the router checks it against
   the map's yard key as it checks a signed call today and makes no
   `GET /user/me` probe. No yard change and no device app change in
   this bead. Where to look: router README ("Push: the relay") and
   its router. Depends on beads 3 and 5, so the statement it checks
   is the one the yard already issues. Done when: a stand-in device
   presenting a voucher writes the same rows the probe wrote, a
   forged or expired voucher writes none, and no device answer
   crosses the edge readable for push; the voucher's own clear
   fields (user, device, yard, expiry) are the register call's
   stated metadata, named as such in the router's proof, not
   mistaken for a concealed identity.
7. **footplate-ios speaks the channel, and migrates in place.** Goal:
   as bead 4 for the iOS app, on the phone pick of section 3.2 (Q7):
   the yard's `flynn/noise` v1.1.0 bound from Go by `gomobile bind`
   for iOS, the binding holding no Noise construction of its own,
   its key in the platform's keeping, an
   already-paired phone registering its key under its token on
   first launch, recorded `sealed (in place)`, and this app's own
   client half of the voucher (beads 5 and 6): on each push
   registration it asks the yard for a fresh voucher inside the
   channel (bead 5's issuance) and presents that voucher at
   `POST /user/push/register` and `unregister`, where bead 6's
   router check makes no probe of it. The bound v1.1.0 and the
   binding are not yet reviewed: Cure53's Turbo Tunnel review
   covered `dnstt` with `flynn/noise` as used there, not this
   version (with `UnsafeNewCipherState`), not this binding, and
   not this design's explicit-nonce and replay framing, and this
   bead does not call them audited (section 3.2, Q7). What
   unlocks the sealed claim on phones is an independent review
   of `flynn/noise` v1.1.0 as bound by `gomobile` and of that
   framing, named as its own later step; this bead builds now
   without waiting for it (mckean's pick, ask 20524), makes no
   audited claim, and shows no sealed claim for a phone — until
   that review passes, the app shows the device as not yet
   sealed, and no public text of this bead's claims more.
   Torann is not built in this
   bead: it stays the unaudited fallback of section 3.2, taken only
   after an independent review if the bound Go artifact proves
   unusable. Depends on beads 1 to 3 and 5 to 6, and on the
   section 3.2 pick — the bound
   `flynn/noise` artifact proved by the specification's vectors
   and a live handshake against the yard and the desktop on the
   iOS 18 floor before channel work starts, those proofs being
   agreement, not review; phone work does not
   start on unaudited code presented as audited. Done when: a phone paired before the
   release and a phone paired after it both call sealed through
   the bound library, each
   registers for push by voucher with no probe made of it, the
   yard's device list tells them apart, and the migrated phone
   shows its lasting `sealed (in place)` label with re-pairing by
   QR offered and never required, while the newly paired phone
   shows `sealed (paired)` with no such line — those labels being
   the yard's record from the day they are earned and, on the
   phone itself, what shows only once the review step above has
   passed; until it passes, the phone's own display says not yet
   sealed in place of either label.
8. **footplate-android speaks the channel, and migrates in place.**
   As bead 7 for the Android app, on the same pick: the yard's
   `flynn/noise` v1.1.0 bound from Go by `gomobile bind` for
   Android on the app's API 29 floor, the bound version and
   binding not yet reviewed and not called audited here either,
   needing the same independent review of v1.1.0 as bound and of
   the explicit-nonce and replay framing, under the same picked
   rule (ask 20524): build now, no sealed claim on the phone
   until that review passes, the phone showing not yet sealed
   until then. Torann likewise held as the
   unaudited fallback and not built, including its own client half
   of the voucher (beads 5 and 6) in the same terms. Depends on
   beads 1 to 3 and 5 to 6, and on the same bound-artifact proof as
   bead 7, on the Android floor. Done
   when: the same two phones' story holds on Android.
9. **A plain call down the link is refused, and the operator's
   sealed device may give every secret.** Goal: from this bead's
   release, a device call that reaches the yard down its edge link
   unsealed is refused, judged on link calls alone, its refusal
   naming the fix; no setting softens it, and the one plain call
   still carried is a pre-release device's one-time migration
   registration, refused to any device that already pins a key. In
   the same release, `GiveSecret` from the operator's own device on
   a sealed call takes every secret as a hosted yard does, with the
   pairing-time warning of section 3.9; every other device is
   unchanged, and a plain call (which is refused here in any case)
   gains nothing. Where to look: `internal/rpc/user.go`
   (`inTheClear`), `docs/edge.md`. Depends on beads 4, 7 and 8
   landed and, beyond that, on their releases being available to
   users on each platform named here — the desktop app's release
   carrying bead 4 available to its users, the iOS app's release
   carrying bead 7 available in its store, and the Android app's
   release carrying bead 8 available in its store — not merely on
   those beads being merged or built. With the phone stores not
   yet live, that availability holds this bead back: it does not
   land until each named release can actually be installed by the
   people who carry it. A client older than those releases loses
   edge access when this bead lands, and keeps only its direct
   paths, until it updates. Done
   when: a plain device through the edge is refused with the fix
   named, its inner call is not served and earns no answer beyond
   the refusal (the test judges what the yard carried out, never
   that the attempt was unread — sent plain, it was readable at
   Cloudflare and the edge, section 3.5), an old app keeps its
   direct paths, a sealed device notices no change, a client that
   has pinned a yard's key sends no plain call on a failed handshake
   and fails sealed instead, the operator's sealed device
   gives a secret beyond the login code and another person's does
   not, no handshake failure produces a plain call, and a member
   who opens the web view's pairing page is told there that pairing
   from that page no longer gives an app that can call through the
   edge and to ask the operator for a QR on the yard's own screen
   (bead 1's page words, sections 3.1 and 3.4; that loss is the
   accepted one, ask 20536, and no self-service member ceremony
   is built by any bead here).
10. **The edge's notice says the new ledger, word for word
   (yardr-hosted).** Goal: in the `yardr-hosted` repository alone,
   `noticePage` trades its device sentences for the sealed ledger —
   what the edge cannot read in a sealed call (calls, answers,
   tokens, codes, screens), what it still reads (webhooks, metadata,
   a plain attempt sent by an app too old to seal, refused without
   being carried out but readable as sent, the member pairing code
   the web view's pairing page shows, the push relay's tokens
   and a voucher's clear fields), and the web view and previews
   named readable in the same type size as the claim they qualify
   (section 5, Q5). `docs/edge.md` in `yardr` is bead 11, not this
   bead. Depends on bead 9. Done when: every sentence of the page
   is true of the build that serves it, checked against the
   router's tests as the notice is today, no sentence claims a
   refused plain attempt was unread, and "codes" in the sealed
   claim is qualified as in section 3.9 — the member pairing code
   the web view's page shows is still read.
11. **The yard's edge account catches up (yardr).** Goal: in the
    `yardr` repository alone, `docs/edge.md` trades its device
    sentences for the same sealed ledger as bead 10, word for word
    where the two pages overlap, including the refused-but-readable
    distinction for plain attempts. Depends on beads 9 and 10, so
    the yard's account copies the served notice, not the other way
    round. Done when: `docs/edge.md` describes only the ledger the
    refusing build keeps, and names the web view and previews
    readable beside the sealed claim.
12. **The yard's user and API documents catch up (yardr).** Goal:
    in the `yardr` repository alone, `docs/users.md` and
    `docs/api.md` describe pairing, the device record and refusals
    as built — the QR's fourth field, `sealed (paired)` against
    `sealed (in place)`, and the refusal a plain call meets.
    Depends on beads 9 and 11. Done when: no document describes a
    `warn` scope that no build has, and none promises the
    unqualified claim for a yard whose refusing release has not
    landed.
13. **footplate's yard documents catch up (footplate).** Goal: in
    the `footplate` repository alone, `docs/yards.md` describes the
    per-yard key, the `sealed to this yard` state and the refusal
    words a pinned client shows when a handshake fails, with no
    plain fallback. Depends on bead 9. Done when: the desktop app's
    documents match the client bead 4 shipped, and no sentence
    describes a plain call through the edge as still available.
14. **yardr.dev takes the sealed sentence (yardr.dev).** Goal: in
    the `yardr.dev` site repository alone, the edge words take the
    sentence bead 9 made true for sealed calls, with the web view's
    and previews' qualification beside it in the same type size,
    and the refused-but-readable sentence for plain attempts
    (section 3.5). Depends on beads 10, 11, 12 and 13, so the site
    copies only wording the served notice and the documents
    already carry. Done when: the site claims nothing for a yard
    whose refusing release has not landed, and claims a refused
    attempt unread nowhere.
15. **Peers through the edge — deferred to a later design.**
    No bead in this programme seals peer calls, and this heading
    builds nothing: peer calls through the edge stay readable to
    the edge, outside this design's guarantee (section 3.6). A
    later design, not this one, must first answer how two yards
    receive and verify each other's X25519 channel key (peer
    pairing today exchanges ed25519 identity keys only; bead 1's
    channel key is a device-pairing key), what happens to peer
    records that already stand, and how such a key is rotated.
    Only then could that design say whether bead 2's envelope is
    reused between yards. Where to look, for that later design:
    `docs/peers.md`, `internal/yardkey/yardkey.go`. Done when, for
    this programme: nothing — the deferral is the decision, and no
    document or notice in beads 10 to 14 may claim peer calls are
    sealed.

16. **A PAKE for typed and read-aloud pairings — the yard
    (yardr).** Goal: in the `yardr` repository alone, the yard side
    of the typed-code pairing: a pairing code typed or read aloud,
    whose code never crossed the edge, is redeemed through a PAKE
    (SPAKE2 or CPace, section 3.1) in place of the QR, exchanging
    the two static keys under the key it derives, by a maintained
    PAKE library in Go under the same rule as question 6 — none of
    our own. No client change in this bead: each client's half is
    its own bead below. Member pairings opened from the app are
    outside this bead's guarantee: the router reads and returns
    that code, so the edge knows the password (section 3.1); a
    member gains the sealed claim by 1B's QR from the yard's
    screen, and an edge-independent member ceremony is a later
    bead only, not built or promised here. Where to look: bead 1's
    pairing, `docs/peers.md` ("What pairing rests on"). Depends on
    bead 1. Done when: this bead has selected one protocol of the
    two (SPAKE2 or CPace) and pinned its suite, its encoding and
    its confirmation rules, so the client beads 17 to 19 implement
    against a fixed choice and do not start before it is pinned;
    the yard redeems a typed code through the
    PAKE against a stand-in client, with the edge relaying and
    rewriting, the pairing holds as 1B's does, a recorded
    transcript buys no offline test of the code, and no member
    pairing opened through the router is described as covered by
    it.
17. **A PAKE for typed and read-aloud pairings — the desktop
    (footplate).** Goal: in the `footplate` repository alone, the
    desktop client's half of bead 16: the same typed or read-aloud
    code redeemed through the same PAKE, by a maintained PAKE
    library in Rust under the same rule — none of our own —
    exchanging the static keys the yard's half (bead 16) expects.
    Member pairings opened from the app stay outside the
    guarantee, as bead 16 states. Where to look: bead 4's pairing
    and call-building code. Depends on beads 4 and 16. Done when:
    the desktop app pairs from a typed code through the real edge
    path as bead 16's yard expects, and a recorded transcript buys
    no offline test of the code.
18. **A PAKE for typed and read-aloud pairings — iOS
    (footplate-ios).** Goal: in the `footplate-ios` repository
    alone, the iOS client's half of bead 16, in the same terms: a
    maintained PAKE library for Swift under the same rule — none
    of our own. Member pairings opened from the app stay outside
    the guarantee, as bead 16 states. Where to look: bead 7's
    pairing code. Depends on beads 7 and 16. Done when: the iOS
    app pairs from a typed code as bead 16's yard expects, with
    the same transcript property.
19. **A PAKE for typed and read-aloud pairings — Android
    (footplate-android).** Goal: in the `footplate-android`
    repository alone, the Android client's half of bead 16, in
    the same terms: a maintained PAKE library for Kotlin under
    the same rule — none of our own. Member pairings opened from
    the app stay outside the guarantee, as bead 16 states. Where
    to look: bead 8's pairing code. Depends on beads 8 and 16.
    Done when: the Android app pairs from a typed code as bead
    16's yard expects, with the same transcript property.

Nothing else is held outside the nineteen headings (eighteen
build beads; bead 15 defers peers and builds nothing). The notice's sealed
sentence is beads 10, 11 and 14's, printable once bead 9's refusal
lands; the
web view's own channel, if one is ever proposed, is a new design,
not a bead of this one.

## Summary (one page)

- **1, key agreement (picked):** QR carries the yard's channel
  key now, code redeemed inside a Noise handshake; PAKE for typed
  codes the edge never saw follows (members stay on QR), compared
  check third, pin-after-plain migration only.
- **2, channel:** method, path, headers, body and inner status
  sealed on `/user/e2e`; outer head shows envelope status only.
  Records numbered, call-bound, finally sealed; past the 65,536
  window a call fails whole; removal recalls nothing sealed. The
  signature refuses outsiders and replays, not the edge: its
  invalid envelopes fail their opening, valid sealed content it
  cannot forge. Device token does not cross; edge session
  (`X-Edge-Token`) stays visible for routing. Path and marker
  bound; beats availability-only. Maintained libraries only, none
  our own (`flynn/noise`; phones bridged now, unreviewed, not
  audited, no phone sealed claim until the review passes;
  `snow`; Torann fallback unaudited).
- **3, what breaks:** push probe becomes a signed voucher (user,
  device, yard, expiry still clear); token kept for pairing,
  channel key authenticates through edge; operator's sealed
  device (picked) may give every secret, others unchanged; web
  view uncovered, same size.
- **4, experience (picked):** no new pairing step or wakeups, one
  handshake at opening; members pair by operator-opened QR, not
  the web view (that loss accepted; self-service is a later
  design). Phones migrate in place as
  `sealed (in place)`: an edge active then could read later calls
  undetected (logging could not); QR re-pairing offered, never
  required; lost phone removed, re-paired.
- **5, optional (picked):** not optional — `require` from the
  first refusing release, no `warn`, no downgrade once pinned.
  Refused plain attempt not carried out, but readable as sent,
  never claimed unread.
- **6, later:** peers deferred (key distribution, records,
  rotation undesigned); hosted yards and web view, no honest
  claim yet.
- **7, order and size:** nineteen headings, six repositories,
  eighteen build beads (15 defers peers); first slice 1 to 4;
  refusal waits for released desktop, iOS and Android clients
  (stores hold it back); PAKE last, one bead per repository (16
  to 19), bead 16 pinning first.

## Sources

All read on 5 November 2026, from each project's own documentation.

- Signal, [Linked Devices](https://support.signal.org/hc/en-us/articles/360007320551-Linked-Devices)
  and [A Synchronized Start for Linked Devices](https://signal.org/blog/a-synchronized-start-for-linked-devices/)
  (27 January 2025): the QR-carried public key and provisioning.
- WhatsApp, *Encryption Overview*, the technical white paper itself at
  [WhatsApp-Security-Whitepaper.pdf](https://www.whatsapp.com/security/WhatsApp-Security-Whitepaper.pdf):
  per-device identity keys and the primary-signed device list.
- Tailscale, [DERP servers](https://tailscale.com/kb/1232/derp-servers)
  (validated 21 January 2026): a relay that blindly forwards
  already-encrypted traffic.
- Matrix, [Client-Server API: End-to-End Encryption](https://spec.matrix.org/latest/client-server-api/#end-to-end-encryption)
  and [Olm & Megolm](https://spec.matrix.org/latest/olm-megolm/):
  per-device keys, verification and cross-signing.
- magic-wormhole, [Welcome](https://magic-wormhole.readthedocs.io/en/latest/welcome.html)
  and [Attacks](https://magic-wormhole.readthedocs.io/en/latest/attacks.html):
  SPAKE2 over a human code through a mailbox relay; Abdalla and
  Pointcheval's SPAKE2 is [RFC 9382](https://www.rfc-editor.org/rfc/rfc9382.html)
  (September 2023).
- Trevor Perrin, [The Noise Protocol Framework](https://noiseprotocol.org/noise.html),
  revision 34: the `IK` and `XX` patterns, the `CipherState` and its
  `SetNonce` (section 5.1), and out-of-order transport messages with
  their rule against accepting an opened nonce again (section 11.4).
  The CPace candidate of section 3.1 is
  [draft-irtf-cfrg-cpace](https://datatracker.ietf.org/doc/draft-irtf-cfrg-cpace/)
  (revision 21, still an active Internet-Draft on 5 November 2026).
  The maintained Noise libraries of section 3.2, each checked that
  day: [flynn/noise](https://github.com/flynn/noise) v1.1.0 for Go
  (the yard, and both phones by bridging), [snow](https://github.com/mcginty/snow)
  0.10.0 (19 July 2025) for Rust, whose README states it has not
  received any formal audit, and [Torann](https://github.com/stonygrey/torann)
  0.3.1 for Swift and Kotlin, whose README states it is not
  production ready and has not been independently audited — the
  phones' unaudited fallback only. The review nearest the phone
  pick is Cure53, [Review-Report Turbo Tunnel Security & Privacy](https://cure53.de/pentest-report_turbotunnel.pdf)
  (March 2021, report dated 14 April 2021), a white-box review of
  `dnstt` with `flynn/noise` as used there, whose `flynn/noise`
  findings were UCB-02-003 (nonce overflow) and UCB-02-004 (the
  deprecated Curve25519 API), fixed in `flynn/noise` v1.0.0; it
  covers neither the pinned v1.1.0 (with `UnsafeNewCipherState`)
  nor the `gomobile` binding below nor this design's
  explicit-nonce and replay framing, so the bound phones are not
  called audited here (section 3.2, Q7); the
  bridge is [`gomobile`](https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile)
  (`golang.org/x/mobile`, `bind` for Android and iOS), checked the
  same day.
- Syncthing, [Relay Protocol v1](https://docs.syncthing.net/specs/relay-v1.html):
  the ends' own TLS inside a joined stream, meaningless to the relay.
- ngrok, [TLS Termination](https://ngrok.com/docs/gateway/domains/tls-termination):
  TLS ended at the cloud service by default, end-to-end shapes named
  as the exception. Cloudflare, [Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/)
  (updated 4 August 2026): TLS ended at Cloudflare, carried onward.
- Nabu Casa, [Remote access — Deep dive](https://support.nabucasa.com/hc/en-us/articles/25619268678557-Remote-access-Deep-dive)
  (updated 9 February 2026): SNI routing of sealed TLS to the
  instance's own certificate.
