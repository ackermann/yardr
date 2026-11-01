# The edge: webhooks with no open port

The edge is the service at `https://my.yardr.dev` that stands in front of
yards. A yard that is connected to it takes its webhooks through it: GitHub,
Linear, Slack and the rest post to
`https://my.yardr.dev/hooks/<account>/<yard>/<hook>`, and the edge hands each
delivery to the yard connected under that account and name. The
yard dials out, so a laptop on any network, with no open port and no tunnel of
its own, is reachable for webhooks and for its devices' calls. What the edge
hands over of a delivery is the sender's raw body and headers, and the yard
verifies every delivery itself, with secrets only it holds. Today the edge
carries webhooks and devices' calls.

```sh
yardr edge connect https://my.yardr.dev   # fetch the edge's key, show it, enrol by a code
yardr edge status                          # what this yard is connected to, and how its link is
yardr edge disconnect                      # leave the edge, and put back the hook address from before
```

A connected yard is a yard you run on your own machine that keeps one link
open to the edge. A hosted yard is a yard the service runs for you
([below](#hosted-and-connected)). Both take webhooks at the same URLs.
Connecting needs a verified account, not the invite: an account the invite
list does not name may connect its own yard, up to the account cap below,
and gets no hosted yard, which still needs the invite list. People who have
no yard of their own
read the same on the edge's public notice,
`https://my.yardr.dev/edge/notice`, which needs no sign-in.

## Connecting a yard

Only the operator connects a yard, and a yard connects to one edge at a time.

```text
$ yardr edge connect https://my.yardr.dev --as airy
The edge's key: ed25519:9bR…T1  (fetched over HTTPS from my.yardr.dev; compare it with https://yardr.dev/edge)
This yard's key: ed25519:Hk3…q9
Open https://my.yardr.dev/connect and enter the code  WQ4F-7K2M
Waiting for you to approve this yard there. Ctrl+C stops, and writes nothing.
… approved by you@example.net as mckean/airy
Connected: webhooks reach this yard at https://my.yardr.dev/hooks/mckean/airy/<hook>
hook_public_url set to https://my.yardr.dev/hooks/mckean/airy
The server opens the link to the edge now: yardr edge status says when it is up.
```

**The first step is trust.** The yard has no edge key yet, so the command
fetches it over HTTPS, with the system's certificate checks, before anything
is signed, and prints it. Whoever holds a valid certificate for the address at
that moment is taken for the edge. A plain `http` address is refused except
on this machine, for a stand-in. After that moment the pin holds on its own:
a different certificate, a different machine behind the name, or a different
key served later moves nothing, and the link refuses an edge that cannot sign
with the pinned key. It is trust on first use, as `ssh` does with a host
key, with the web's certificates making the first use hard to intercept. A
person who does not want to take the certificate's word compares the printed
key with the one published at `https://yardr.dev/edge`, a page outside the
edge's own hosting.

**Then the yard enrols.** It sends its public key and the name it proposes
for itself at the edge — the yard's own name, or `--as <name>` to choose
another, in lower case letters, digits and `-` — signed to the pinned key,
and gets a code back. You sign in at `/connect` and enter the code, and the
page shows the proposed name and the start of the yard's key before you
approve it, and you may change the name there; what the edge answers back,
your account's handle and the yard's name, is what is kept. You type and
paste nothing on this machine: the yard's key is its credential, and every
answer from the edge is checked against the pinned key.
Connecting needs a verified account, not the invite: an account the invite
list does not name may connect its own yard, up to the account cap below,
and gets no hosted yard, which still needs the invite list. An account whose
email is not verified is refused.

**Connecting sets two settings.** The `edge` setting holds the edge's
address, its pinned key, the account's handle and this yard's name, and the
`hook_public_url` it replaced
([configuration.md](configuration.md#settings)). `hook_public_url` becomes
`<url>/hooks/<account>/<yard>`, so `yardr hook add` and the packs that
register their own webhooks print or register the right URL with no change.
The running server dials at once; no restart. The web process (`yardr web
start`) keeps a second socket with `Yardr-Link-Role: web`, serving the web
view's requests and event streams through its own handler. Both sockets use
the same pinned edge key and yard key and follow the `edge` setting.
`edge_link_up` and `edge_link_down` events carry `role` (`yard` or `web`),
with why a link went down. Only the yard link writes `edge.state`:
`yardr edge status`, `yardr status` and `yardr doctor` show the yard link,
not the web link.

The edge no longer accepts the previous one-segment webhook URL
`/hooks/<handle>/<hook>`. A setting saved before yards had names reads its
yard name as the account handle, so update `hook_public_url` and each
registered webhook URL to `/hooks/<handle>/<handle>/<hook>`.

**Disconnecting puts it back.** `yardr edge disconnect` asks the edge to drop
its row for the yard, clears `edge`, and restores the `hook_public_url` from
before connecting, or clears it when there was none. One you changed after
connecting stays as you left it. The settings are cleared whether or not the
edge answers; when it does not, the command says so, and its row waits for
its operator.

## What the edge reads, and what it never holds

TLS ends at Cloudflare and at the router, so the edge reads every webhook
payload and its headers, and the yard's answer, in the clear. A device's
calls pass it in the clear too
([below](#footplate-through-the-link)). The yard-key
signatures prove who sent a frame; they hide nothing. Nothing through the
edge is end-to-end encrypted.

It never holds:

- **repositories**: git runs from the yard to GitHub directly, never through
  the link;
- **hook signing secrets**: they are in the yard's secret files, and the yard
  verifies every delivery itself; the edge cannot verify one;
- **the yard's private key**: the key never leaves the yard's machine;
- **model credentials of a connected yard**: the harness logs in on the
  person's machine.

For a hosted yard the service keeps no harness credential either: the person
signs each login in, and states at sign-up that their provider's terms allow
that use. The edge's public notice says the same for both kinds of yard, for
people who have no yard of their own.

Peers are carried through the edge when a yard is called at its peer URL,
`https://my.yardr.dev/peers/<account>/<yard>`: the edge passes the requests
down the yard's link and the answers back, keeping nothing of either
([peers.md](peers.md#linking-two-yards)). It reads them as it reads a
webhook: the peer signatures prove who sent what, and hide nothing. Two
yards that reach each other directly still call each other directly. A
device's calls are carried through the same link
([below](#footplate-through-the-link)).

## footplate through the link

A device of yours, such as footplate, the native window over a yard, reaches
a connected yard through the same link, with no listener of the yard's own.
`yardr user device pair <user> footplate` counts a link that is up as a
listener and gives the edge's address with the code, beside this yard's
handle and name at the edge, so pairing is pairing by code as on the LAN:
the device is given the address, the yard's handle and name and the code,
and asks once for its token ([users.md](users.md#enrolled-by-a-code)). The
device says which of the account's yards it means with each call, in
`X-Edge-Yard`. For that one call the edge also names the account that
presents it, in `X-Edge-Presenter`, and the yard redeems the code for the
user the pairing was opened for alone.

Two gates, as for a hosted yard: the edge's (your account's session) and the
yard's (the device's token). Every call but the stream waits up to 30
seconds for the yard's answer; past that, or with no link up, the edge
answers 504 or 503 and keeps nothing: a device asks again, and only
webhooks are queued. `GET /user/events` stays open as it does on the
listener; while nothing arrives for 30 seconds the edge writes an empty
line, which is no event and which a client skips
([api.md](api.md#the-stream-of-a-device)).
An answer larger than one frame of the link, such as a blob download,
crosses as a stream — the head, then the body in pieces — up to the
edge's 16 MiB bound; past it the edge cuts the response.

What the edge reads of it: every call and its answer — conversations, beads,
mail, asks, notes — and the device token each call carries, with which the
edge could act as that device within what the device may do. The edge is
ours, and connecting needs a verified account, not the invite: an account
the invite list does not name may connect its own yard, up to the account
cap below, and gets no hosted yard, which still needs the invite list. A
token is revoked per device on the yard: `yardr user device rm <user> <device>`.

Approvals cross the link: `ApproveAsk` and `ApproveSetup` are taken from a
paired device of the operator down the link, as from the yard's own
machine — pairing is the grant, and `yardr user device rm` is the revoke.
A setup approval that types the account is the exception on a connected yard,
since what is typed is a secret (below). One secret crosses with them on every
yard: the code a harness's sign-in page shows, when the yard leads the
harness's login for the operator
([crew.md](crew.md#the-yard-leads-a-harnesss-login)). A paired device of the
operator gives it with `GiveSecret`, so a phone with no terminal behind it
can sign the assistant in. The edge reads the code with the device's token,
as it reads every call. The code is used once, within minutes, and the yard
hands it to the waiting login in its memory and keeps it nowhere.
A session's screen crosses with the approvals: `SessionScreen` is taken from
a paired device of the operator down the link, as from the yard's own
machine. The edge reads the screen with the device's token, as it reads
every call; a member's device is answered as for a method the yard does not
have.
Teammates cross with them too: `Teammates` and `UninviteTeammate` are taken
from a paired device of the operator down the link, as from the yard's own
machine, so the operator's paired device reads the yard's teammates and
files a removal through the edge.
On a connected yard no other secret crosses: model credentials such as an
API key, and hook signing secrets, are answered at the yard's own machine, as
on a LAN without TLS, and `GiveSecret` is refused down the link for every
other ask. So is `ApproveSetup` with the account typed (`values`), which is
answered at the yard's own machine too (`yardr bead answer <bead> <ask> <n>
--secret`); a setup approved by taking a candidate or keeping the account types
nothing and crosses with the other approvals. On a hosted yard every secret
crosses, from a paired device of the operator: the service runs the edge and
the machine the yard lives on, and the operator has no shell there to answer
at (the yard's `hosted` setting,
[configuration.md](configuration.md#settings)). A device of a user who is not
the operator gives no secret down the link on either. The grant list,
`SourceGrants`, carries no credential but is never read through the edge, on
either. A device call cannot read or change the telemetry switch through the
yard link: `TelemetryShow` and `TelemetrySet` are refused there. A hosted
customer uses the web view's control over its separate `web` link; the view
checks the gateway's JWT and acts as the mapped yard user.

## Hosted and connected

| | hosted yard | connected yard |
|---|---|---|
| account, handle, sign-up | the edge's, by invite | the edge's, by verified sign-up |
| webhook URL | `https://my.yardr.dev/hooks/<account>/<yard>/<hook>` | the same |
| peer URL | `https://my.yardr.dev/peers/<account>/<yard>` | the same |
| how the edge reaches it | TCP to its container or droplet | the link the yard opened |
| footplate | Kratos session plus device token, pairing by code | the same, through the link |
| secrets given from a device | every secret, from the operator's devices, the account a setup types among them | the code of a harness login |
| who runs it, upgrades, backs up | the service | the person |
| invite list | yes, for the trial | no: any verified account connects its own yard, and gets no hosted yard |

One account holds up to three yards in the trial, hosted and connected
side by side, each under a name of its own. A fourth yard is refused, and
so is a name the account already has a yard under. A connected yard leaves
with `yardr edge disconnect`; a hosted yard is deleted by the operator on
request.

**Amended: a hosted yard has a name of its own.** A hosted yard takes the
account's own name unless its person gives it another at `/ready`, and the
cap of three counts hosted and connected yards of any name. To change a
yard's kind, leave it first — `yardr edge disconnect` for a connected yard,
the operator's delete for a hosted one — then choose the other kind under
the same name. Nothing converts in place and no data moves. The hook URLs
carry the account and the yard's name, so they are the same across the
change. The deliveries the edge kept for a connected yard follow its name
and account and not its kind, but only a connected yard's link reads them,
so a hosted yard under a former connected yard's name inherits none.

The web view follows the browser's yard. A `GET` or `HEAD` of any page
with `?yard=<name>` makes that yard the browser's: the edge checks that
the yard is the session's account's, sets the cookie `edge_yard` (path
`/`, `HttpOnly`, `SameSite=Lax`, `Secure` behind TLS, a session cookie)
and answers `303` to the same page without the parameter. A browser with
no cookie is for the yard named as the account. A name that is no yard of
the account is refused `403`, from the parameter or the cookie, and the
edge does not fall back to another yard. The yard is shown neither the
cookie nor the parameter. A device's calls still pick their yard with
`X-Edge-Yard` and ignore the cookie.

The cost is that a cookie is the browser's and not a tab's: two tabs on
two yards of one account share it, and the last `?yard=` visit decides
for both. A subdomain per yard is the later answer and stays left out.

## While the yard is away: the queue

The edge forwards live first: with the yard connected, it sends the delivery
down the link and waits up to 2 seconds for the answer, which it passes back
to the sender byte for byte. That keeps the yard's own answers working: a
handshake answered from the verified body, a 401 for a bad signature, a 200
for a dropped stranger. With the yard not connected, or no answer in time,
the edge keeps the delivery and answers the sender `202` with the queued
entry's id. GitHub does not redeliver on its own, so keeping it is the only
way it is not lost. On reconnect the edge sends the queue oldest first; the
yard serves each and sends `ack` once it is recorded or refused, and only
then does the edge delete it. A live frame that ran out of time is the same
queued entry: if the yard's answer arrives later, it counts as that entry's
ack.

**What bounds the queue.** Anyone on the internet can post to a hook URL, and
the edge cannot tell a real delivery from junk, since only the yard holds the
secret. So every bound is checked before the `202`, and a request past one is
refused, never kept:

- **a body of at most 1 MiB**, the yard's own limit; a larger one is answered
  `413`, live or away;
- **per yard, at most 1,000 deliveries and 64 MiB queued**, and nothing kept
  past 7 days;
- **across the edge, at most 2 GiB queued**, so one yard's flood cannot fill
  the disk the edge lives on.

Past a per-yard or edge-wide bound the edge answers `503` with
`X-Edge-Refusal: queue-full` and keeps what it has: the newest is refused
rather than the oldest dropped, so a stranger's flood pushes no real delivery
out. Entries older than 7 days are dropped. The numbers are the router's
flags.

**Junk in the queue, accepted for the trial.** The bounds cap what a stranger
can cost the edge, but not what they can cost one yard: while a yard is away,
anyone who knows its account and yard name can post junk to its hook URLs
until its queue is full, and its real deliveries are then refused until the
junk is delivered (and refused by the yard) or expires. That risk is accepted
for the trial,
with two guards: the [Cloudflare rule](#the-edges-operator-the-cloudflare-rule)
below, which slows any one sender, and an event to the edge's operator, at
the first refusal for a yard and then at most one an hour, through the
router's notify command or its log. Each refusal and each drop is counted on
the yard's row.

**Twice, and caught.** A delivery can reach the yard twice: an ack lost with
the link, or a late live answer beside the queued copy. The yard catches the
second with its existing dedupe, which holds for every delivery it records:
each has a key, the sender's delivery id or a body the source declares
unique. A source whose bodies repeat, a `token` hook for one, takes no
delivery that names no id, so the sender sets `Yard-Delivery` and a delivery
without one is refused (and acked, so the edge does not send it again). The
edge does not dedupe: judging which two bodies are one delivery is the yard's
knowledge.

**A queued delivery's timestamp.** A declared source may refuse a delivery
whose timestamp is further than its window from now, which would fail every
queued delivery on arrival. So for a queued frame the yard checks the
sender's timestamp against the edge-signed time the edge received it, instead
of its own clock. The window keeps its job at the edge's door instead of the
yard's, with the stated limit that the edge could replay a captured delivery
with a fresh time.

**Save a Slack URL with the yard connected.** Slack sends `url_verification`
when the URL is saved in its app settings, and expects the challenge back in
the answer. The edge never answers a handshake, since it cannot verify one,
so one that arrives while the yard is away is queued and answered `202`, and
Slack shows the URL as not verified. Save the URL with the yard connected,
which is when the hook is being set up anyway, and save it again if Slack
refuses.

## The edge's operator: the Cloudflare rule

The rule is set in Cloudflare's dashboard, not in code: zone `yardr.dev` >
Security > WAF > Rate limiting rules > Create. It is the zone's one
rate-limiting rule. The zone is on Cloudflare's Free plan, whose
rate-limiting rules match on the path alone, count by IP, and count and block
over a fixed 10 seconds:

- **expression:** `starts_with(http.request.uri.path, "/hooks/")`
- **count by IP**, 100 requests per 10 seconds
- **action:** Block for 10 seconds

The period and the block are the Free plan's fixed 10 seconds; the count is
proposed. It covers `/hooks/` on every host of the zone, not only
my.yardr.dev; no other host of the zone serves `/hooks/` today, so the wider
scope costs nothing now, and whoever adds one reads this first.

The rule is best effort. Cloudflare counts requests per data center, and
enforcement can lag
(`https://developers.cloudflare.com/waf/rate-limiting-rules/request-rate/`),
so a short burst past the count still reaches the edge. That fits the
queue-abuse risk accepted for the trial above. The rule is set before a yard
is pointed at the edge, so junk is throttled before it reaches the queue.
