# One edge for hosted yards and for yards that connect out to it

Design yardr-hosted-050n, proposed 2026-10-20. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. The operator is the person who runs it. A device is one program of a user that calls the yard from outside it, such as footplate, the native window over a yard. The hook listener is the yard's one network listener, set by `hook_addr`, serving `/hooks/`, `/peers/` and `/user/`. The edge is the service at https://my.yardr.dev that stands in front of yards: today Kratos (accounts), Ory's reference sign-up pages, a provisioner and our own router, in the repository `yardr-hosted` (design yardr-bwm2). A hosted yard is one we run for a person, in a container or a droplet of their own. A connected yard is new in this design: a yard the person runs on their own machine that keeps one outbound connection open to the edge, which we call the link. See docs/glossary.md for the rest of yardr's words.

## 1. The question

mckean, 20 Oct 07:20, after the strategy document "Yardr — Monetization & Licensing Strategy" (revised 18 Oct): free to run yourself, paid to stay connected; "reminds me of obsidian" (a free local app, paid Sync). What is new: "a self-hosted yard gets easy hosted webhooks and on-the-go connectivity without open ports." Today the yard takes webhooks on `0.0.0.0:8790`, which needs a reachable port or a tunnel; footplate pairs on the LAN, or through the edge only for a hosted yard; peers link over networks the user controls. The design is to say what carries the yard's outbound connection to the edge, how it authenticates with the yard key and how it reconnects; how the edge gives each yard a URL per hook and forwards the raw body and headers so the yard verifies signatures itself, with signing secrets never at the edge; how footplate and the web view reach the yard through the same connection; what the edge can read (webhook payloads and yard events, said plainly, not claimed end-to-end) and what it never holds (repositories, model credentials); how it fits the hosted sign-up and the Google mint of yardr-hosted-4dx0; and the smallest first slice. At 07:30 he added: "hosted yards are in the offering too", so one edge serves both kinds; the design says what the two share (sign-up, accounts, webhook URLs, footplate pairing) and where they differ, and for hosted yards carries the strategy's limit, API keys or provider plans whose terms allow hosted automated use, and how sign-up checks it. At 07:40 (relayed by the yardmaster): the connection could be a separate app, perhaps TypeScript; it must work with no port forwarding and on laptops that change networks. Left out by the bead: billing, teams and Junction. Kept: mckean's rule for networking (memory mem-stdz), "define what to allow, … we're not building nginx"; this is a service we run, not proxy configuration in the yard, and the design says how it keeps that line.

## 2. What exists today

### 2.1. The edge already routes webhooks per yard, by path

`yardr-hosted/router/internal/router/router.go`, `serveHooks`: a request to `/hooks/<handle>/<hook>` looks the handle up in the provisioner's map (`internal/edgemap/map.go`, a SQLite file read through `sqlite3`; rows carry `Backend` `docker` or `droplet`, `Target`, `State`), strips `/hooks/<handle>`, and proxies to that yard's hook listener at `http://<target>:8790` (`DefaultYardBase`). Webhook deliveries carry no session by design; the yard verifies them. Every other yard route (the web view, `/user/*`) needs a Kratos session: the browser's cookie, or footplate's session token in `X-Edge-Token` (design bwm2, 3.6), and the router mints a one-minute JWT for the web view (`internal/edgejwt`). An unknown or not-ready handle gets 404. The edge runs on mckean's Mac in Docker Compose behind a Cloudflare tunnel (memory mem-0khv); the map, invites and Kratos live there too.

### 2.2. The yard verifies every webhook itself, with secrets only it has

`internal/hooks/receiver.go`, `Receiver.serve`: `POST /hooks/{name}` reads the hook's secret file on every delivery (`config.ReadSecretFile`), verifies the signature through the source's adapter (`ad.Verify`), answers a verification handshake from the verified body at once (`ChallengeAnswer`, as Slack's `url_verification` needs), then normalises, dedupes (`DedupeKeys`, by the sender's delivery id) and records. A declared source can also check the sender's timestamp against the yard's clock: `internal/hooks/declared.go`, after the signature, refuses a delivery whose timestamp is further than `window` from now. Two shipped packs set it: `packs/linear/linear.yaml` (`window: 1m`) and `packs/slack/slack.yaml` (`window: 5m`). This matters for any queue in front of the yard (3.4).

### 2.3. Where a hook's URL comes from

`hook_public_url` (`internal/store/settings.go`, `HookPublicURLSetting`, checked by `CheckHookPublicURL`: https, no user, query or `..`) is the public address hooks are reached at; a hook's URL is it plus `/<name>`. Packs that register their own webhooks read it (`internal/store/runask_hook.go`, `hookPublicURL`). A hosted yard sets it to `https://my.yardr.dev/hooks/<handle>`.

### 2.4. The yard key

Every yard has an ed25519 key at `~/.yardr/peer.key` (`config.PeerKey`, made by `yardkey.Ensure` on first use; `yardr peer key` prints the public half). `internal/yardkey` signs requests (`SignRequest`: method, path, time and body hash, to a named receiver key) and answers (`SignResponse`), carried in `Yardr-Peer-Key`, `Yardr-Peer-Time` and `Yardr-Peer-Signature`, with `MaxSkew` of five minutes. Its `Invite` says "A yard without a URL cannot be called: it calls", which is the shape of a connected yard. Peers (docs/peers.md) sign everything and leave TLS to whatever stands in front.

### 2.5. Devices and the listener

`yardr user device pair <user> <device> --url <address>` (internal/cli/devicepair.go; docs/users.md, "Enrolled by a code") opens a ten-minute code the device redeems at `POST <address>/user/pair`; it refuses on a yard with no listener. The device then calls the 21 calls of docs/api.md under `/user/` with its token. `internal/rpc/user.go`, `inTheClear`: a secret (`GiveSecret`) is taken only from a loopback peer, or from the hosted router named by `router_addr`; approvals (`ApproveAsk`, `ApproveSetup`) only from loopback. The source address is `r.RemoteAddr`, never a forwarded header. footplate's hosted mode signs in with Kratos and sends both tokens (bwm2 3.6).

### 2.6. The listener is one mux, served in `yardr server`

`internal/server/server.go`, `serveHooks`: when `hook_addr` (or `YARDR_HOOK_ADDR`) is set, the server listens there and serves one `http.ServeMux` holding the hook receiver, the peers receiver and the device server. When neither is set, nothing listens. The web view is another process (`yardr web serve`, on `web_addr`, loopback unless `--unsafe`), and in JWT mode verifies the edge's token (`web_jwt_*` settings, design yardr-scpj).

### 2.7. The hosted terms and the Google app

Design bwm2's D2 (decided): hosted users bring their own harness credential, the service keeps none, and the led-login relay carries a login input in memory as a stated trial limit. Design yardr-8wxe 2.10 read Anthropic's terms on 12 Oct: a service hosting Claude Code agrees to the Commercial Terms; each end user authenticates with their own API key, subscription or third-party provider credential; advertised Pro and Max limits "assume ordinary, individual usage". Bead yardr-hosted-4dx0 (backlog, not drafted) is a hosted Google OAuth app: a person connects Google in the browser, hosted keeps the refresh token encrypted, and a hosted mint, through the existing `mint_command` seam, serves the google packs.

## 3. The proposal

The edge that exists carries most of it: the account, the handle, the map, `/hooks/<handle>/*`, the session check on `/user/*`, footplate's hosted mode. What is new is a third kind of map row, `connected`, whose transport is a WebSocket the yard opened, instead of a TCP dial to a container or droplet; the endpoint in the router that takes that socket; a queue for deliveries that arrive while the yard is away; and on the yard's side one command and one setting that make the yard dial out and serve what arrives with the handlers it already has.

### 3.1. The picture

```text
GitHub, Linear, Slack ──POST /hooks/<handle>/<hook>──▶ my.yardr.dev router ─┬─ docker row:    http://yard-<handle>:8790   (today)
footplate ──/user/* + X-Edge-Token──────────────────▶   (session check,     ├─ droplet row:   http://<tailnet>:8790       (today)
browser ──/ + Kratos cookie─────────────────────────▶    map lookup)        └─ connected row: the yard's open WebSocket   (new)
                                                                                         ▲
                                         laptop, any network, no open port:              │ wss://my.yardr.dev/edge/link
                                         yardr server ── link ── dials out on 443 ───────┘ signed with peer.key
                                             └─ serves each request that arrives with the hook listener's own mux, in process
```

### 3.2. The link: a WebSocket the yard dials, signed with the yard key

**What carries it.** One WebSocket from `yardr server` to `wss://my.yardr.dev/edge/link`, on 443, through whatever the laptop's network allows outbound. A WebSocket and not long polling, because the edge must send requests down to the yard at any moment and get answers back (footplate's calls, later the web view), and because both Cloudflare's tunnel and Cloudflare Workers carry WebSockets natively. Each message is one JSON frame:

```text
edge → yard  {"type":"request","id":"r-81","method":"POST","path":"/hooks/github",
              "header":{"X-Hub-Signature-256":["sha256=…"],…},"body":"<base64, raw bytes>",
              "received":"2026-10-20T09:14:03Z","queued":false,"sig":"<edge signature>"}
yard → edge  {"type":"response","id":"r-81","status":200,"header":{…},"body":"<base64>"}
yard → edge  {"type":"ack","id":"r-81"}            (a queued delivery is done with: the edge deletes it)
either way   {"type":"ping"} / {"type":"pong"}
```

Requests are multiplexed by `id`, so a slow delivery does not hold up a footplate call. The body is the raw bytes the sender sent, and the headers are the sender's own, minus the hop headers and the edge's own (`X-Edge-Token`, `X-Edge-JWT`, `Cookie`), so the yard's adapter verifies the signature exactly as it does on the listener. The edge never holds a hook's signing secret and never verifies a delivery; it cannot.

**How it authenticates.** Both ends have ed25519 keys and each pins the other's at enrolment (3.5). The yard opens the socket with a `GET /edge/link` signed by `yardkey.SignRequest` to the edge's public key, the three `Yardr-Peer-*` headers as between peers; the edge finds the `connected` row by the key, checks the signature and `MaxSkew`, and answers `101` with `Yardr-Peer-Signature` from `SignResponse`, which the yard checks against the pinned edge key before it serves anything. TLS (Cloudflare's) protects the wire; the signatures say who is at each end even if a name or certificate is wrong. Every `request` frame carries the edge's signature over its id, method, path, body hash and `received` time, so a frame on a socket that is somehow not the edge's is refused, and so the yard can trust `received` (3.4).

**How it reconnects.** A laptop that changes network, sleeps or loses Wi-Fi often kills the socket silently: neither side sees a close. So each side sends `ping` every 30 seconds and counts the socket dead after 75 seconds with nothing heard (under the roughly 100 seconds after which Cloudflare's proxy closes an idle connection), and the yard dials again with backoff: 1 second, doubling to 60, with jitter, reset after a link that stayed up for a minute. The edge keeps one socket per yard, newest wins: a second signed connect from the same key closes the older one, which is what a laptop that woke on a new network does. Requests in flight on a dead socket are answered as in 3.4.

**What the yard does with a request.** It hands it to the same `http.ServeMux` `serveHooks` builds today (2.6), in process, with `RemoteAddr` set to `edge:0`, an address that is never loopback and never `router_addr`. So a hook delivery meets `Receiver.serve`, a device call meets the device server with its own token check, `inTheClear` refuses a secret or an approval that came over the link, and nothing new decides who is let in. `/peers/` is not served over the link in this design (section 4). The link needs no `hook_addr`: a laptop yard can have no listener at all and be fully reachable through the edge.

### 3.3. Webhooks: one URL per hook, the same as a hosted yard's

The URL is the one hosted yards already have: `https://my.yardr.dev/hooks/<handle>/<hook>`. Connecting sets `hook_public_url` to `https://my.yardr.dev/hooks/<handle>`, so `yardr hook add` and the packs that register their own webhooks print or register the right URL with no change. The router's `serveHooks` keeps its lookup and gains one branch: for a `connected` row it sends the request down the yard's socket instead of dialling a target.

### 3.4. A delivery when the yard is away

The edge forwards live first: with the yard connected, it sends the frame and waits up to 8 seconds for the response, which it passes back to the sender byte for byte (status, headers, body). That keeps the yard's own answers working: a Slack handshake answered from the verified body, a 401 for a bad signature, a 200 for a dropped stranger.

With the yard not connected, or no answer within 8 seconds, the edge stores the request (raw body, headers, `received`, an id) in a queue per yard and answers the sender `202` with `{"queued": "<id>"}`. GitHub does not redeliver on its own, so answering and keeping is the only way the delivery is not lost. On reconnect the edge sends the queue oldest first with `"queued": true`; the yard serves each as above and sends `ack` once it is recorded or refused, and only then does the edge delete it. A delivery sent twice (an ack lost with the socket) is caught by the yard's existing dedupe on the sender's delivery id (`DedupeKeys`, 2.2). The queue is bounded per yard (proposed: 1,000 deliveries or 7 days, oldest dropped first, the drop counted on the yard's row and told to the yard on its next connect as an event).

The timestamp window (2.2) breaks this for Linear (1 minute) and Slack (5 minutes): a delivery that waited an hour fails `Verify` on arrival. So for a frame with `"queued": true` and a valid edge signature, the yard checks the sender's timestamp against the edge-signed `received` time instead of its own clock. The window keeps its job, refusing a capture replayed later, at the edge's door instead of the yard's: the edge could replay a captured delivery with a fresh `received`, which is said as a limit, since the edge is ours and holds the payloads anyway (3.7). This is open question 2.

### 3.5. Connecting a yard: one command, approved in the browser

The account is the hosted one: a Kratos identity with a verified email and a handle (bwm2 3.2). On the yard:

```console
$ yardr edge connect https://my.yardr.dev
This yard's key: ed25519:Hk3…q9
Open https://my.yardr.dev/connect and enter the code  WQ4F-7K2M  (10 minutes)
… approved by chris@mckean.co as mckean
Connected: webhooks reach this yard at https://my.yardr.dev/hooks/mckean/<hook>
hook_public_url set to https://my.yardr.dev/hooks/mckean
The link is up (yardr edge status).
```

The command posts the yard's public key and its name, signed, to `/edge/enrol`; the edge answers with a code and its own public key; the person, signed in at `/connect`, types the code and sees the yard's name and the start of its key before approving; the command, waiting on the same signed call, gets the handle back. It writes the setting `edge` (the edge's URL, its public key and the handle, one value) and `hook_public_url`, and the running server dials. The edge writes the row through the provisioner's `register`, as for any yard: backend `connected`, target empty, the yard's public key, state `ready`. This is the device-code shape every TV app uses, and it means no token is ever pasted or kept on the yard: the yard key is its credential.

```console
$ yardr edge status
edge      https://my.yardr.dev  (key ed25519:9bR…T1, as handle mckean)
link      up since 09:12 (3 reconnects today; last: network changed at 08:47)
queued    0 waiting at the edge, 41 delivered after a reconnect since connecting
$ yardr edge disconnect      # closes the link, clears the two settings, asks the edge to drop the row
```

`yardr status` and `yardr doctor` show the link's line beside the listener's; the server logs `edge_link_up` and `edge_link_down` events (with the reason: closed, unanswered pings, refused key) so a crew member can be told.

### 3.6. footplate and the web view through the same link

**footplate, in the first slice after webhooks.** Nothing changes in footplate: its hosted mode already signs in with Kratos and sends `X-Edge-Token` beside the device token (bwm2 3.6). The router's session half gains the same `connected` branch: `/user/*` from a session whose row is `connected` goes down that row's socket. `yardr user device pair <user> footplate` counts an up link as a listener and takes the edge's address as the default `--url` (`https://my.yardr.dev`), so pairing is pairing by code as today. Two gates, as for a hosted yard: the edge's (the account's session) and the yard's (the device token). Over the link, `inTheClear` refuses `GiveSecret` and the approvals (3.2); the person answers those at the yard's own machine, as they would on a LAN without TLS. Whether a connected yard should take secrets over the link as a hosted yard takes them from its router is open question 4.

**The web view, later.** It is another process on loopback (2.6), so the server's link cannot serve it in process. Of the two ways, the one that keeps mem-stdz's line is that `yardr web serve` opens its own link, with the same key and the same handle, marked as the view's, serving its own handler in process, in JWT mode with the edge's keys; the other, the server's link proxying to `127.0.0.1:8791`, is a proxy inside the yard. Neither is in the first two slices.

### 3.7. What the edge can read, and what it never holds

Said on the connect page and in docs/edge.md, plainly:

- **It can read** every webhook payload and its headers (TLS ends at Cloudflare and at the router); every footplate call and answer: conversations, beads, mail, asks, notes; later every web-view page. The queue keeps webhook payloads at rest on the edge for up to the queue's bound. Nothing through the edge is end-to-end encrypted; the yard-key signatures prove who sent a frame, they hide nothing.
- **It never holds** repositories (git runs from the yard to GitHub directly, never through the link); hook signing secrets (they are in the yard's secret files, and the yard verifies); the yard's private key; model credentials of a connected yard (the harness logs in on the person's machine, and secrets are refused over the link). For a hosted yard, bwm2 D2 stands as decided: the service keeps no harness credential, and the led-login relay carries a login input through it in memory, stored nowhere.
- **It holds by the person's choice** the Google refresh token, when they connect Google through yardr-hosted-4dx0's app. That is the one credential the edge keeps, encrypted, revocable by the person, and the reason a yard would use the hosted mint at all.

### 3.8. Hosted and connected: what they share and where they differ

| | hosted yard | connected yard |
|---|---|---|
| account, handle, sign-up | Kratos, the same pages | the same |
| map row | `docker` or `droplet`, made by the provisioner | `connected`, made by `yardr edge connect` |
| how the edge reaches it | TCP to the container or droplet | the WebSocket the yard opened |
| webhook URL | `https://my.yardr.dev/hooks/<handle>/<hook>` | the same |
| footplate | Kratos session plus device token, pairing by code | the same |
| web view | JWT from the router | later (3.6) |
| who runs it, upgrades, backs up | the service | the person |
| harness credential | the person's, limited to terms that allow hosted automated use (below) | the person's, on their own machine; the service is not running it |
| secrets over the edge | from the router (`router_addr`) | refused over the link (open question 4) |
| Google | the hosted mint (4dx0) | the hosted mint, if they connect Google |
| invite list | yes, for the trial | open question 5 |

**The hosted limit and how sign-up checks it.** The strategy's line is that a hosted yard runs on API keys, or provider plans whose terms allow hosted automated use. Sign-up checks it the only way a service that keeps no credential can: by statement, and by what the yard offers. The account page that offers a hosted yard lists the allowed credential ways per harness kind, kept in one file in `yardr-hosted` (`provision/credentials.yaml`, for example `claude: [api_key]`), and a hosted yard is made only after the person ticks that they will use one of them. In the yard, the hosted yardmaster's role, which leads the login relay (bwm2 3.5), offers only the listed methods at `choose`. Nothing reads or tests the key: probing it would be handling it, which D2 rules out. How strict the list is, and whether Claude subscriptions are on it, is open question 3.

**Google (yardr-hosted-4dx0).** The hosted mint takes yard-key-signed calls: a yard, hosted or connected, runs the google pack's `mint_command` as a small command that calls `POST https://my.yardr.dev/mint/google` signed with `peer.key`, and the edge finds the account by the key and answers with a short-lived access token. The same enrolment that connects the link gives the mint its caller identity, with no second token. This design only fixes that contract; 4dx0 designs the app.

### 3.9. Where the edge's side runs: Go, in the router

The router already does the edge's whole job for two backends: the session check, the map, `/hooks/<handle>`, the JWT, footplate's refusal shapes. The link is a third backend of the same router, in Go, with the queue in a SQLite file beside the map (the router's own, written by the router, since the queue is the router's state and not the provisioner's). Signatures come from the same ed25519 code yardr uses, copied or imported as a small module. mckean's TypeScript idea fits Cloudflare Workers with a Durable Object per yard (socket and queue in one object, nothing for us to keep running), and it is the stronger choice once the edge leaves the Mac; but it would split one edge into two services that must agree on accounts, sessions and the map. This is open question 1.

### 3.10. The line mem-stdz draws, kept

The yard gains no listener, no TLS, no proxy rules and no knowledge of tunnels or networks. It gains one outbound setting, `edge`, naming one service by URL and key, which it dials and nothing else; requests that come down the link are served by the handlers the listener already has, in process, never forwarded to another address or port. What is allowed is still said by the yard's own rules: hooks by their secrets, devices by their tokens, secrets and approvals only from loopback. Everything that is relay, queue, TLS and routing lives in a service we run, outside yardr.

### 3.11. The first slice

One connected yard, mckean's laptop yard, webhooks only, live: E1 to E4 of section 6. Done when a GitHub push to one of his repositories reaches his laptop yard through `https://my.yardr.dev/hooks/mckean/github`, with the laptop on a network with no open port, and keeps arriving after he moves the laptop from home Wi-Fi to a phone hotspot. The queue (E5, E6) follows at once, since a laptop is away often; footplate through the link (E7) after that.

## 4. Left out

- **Billing, teams and Junction.** Left out by the bead. The `connected` row is where a paid flag would sit later.
- **The web view through the link.** Its own process; 3.6 names the way that keeps the line, after footplate.
- **Peers through the link.** Two connected yards could link through the edge (`/peers/` frames), but peers today are the operator's own networks with signatures end to end; routing them through our service changes who can see their mail, and nobody asked for it.
- **End-to-end encryption between footplate and the yard.** Possible with a key footplate and the yard share at pairing, so the edge carries only ciphertext for `/user/*`, but webhooks cannot be (the sender encrypts for TLS only), and footplate's hosted mode would change. The bead asks to say what the edge reads, not to hide it.
- **A queue for footplate calls.** A device call to an away yard gets `503` with `X-Edge-Refusal: away`; only webhooks are kept, since only their senders do not try again.
- **Yards on several accounts, or several yards on one account.** One account, one yard, one handle, as hosted v0. A second yard per account is a later row shape.
- **The edge running anything for the yard.** It answers no handshake, verifies nothing, and never writes to a yard except through the frames above.

## 5. Open questions

1. **Where does the edge's half of the link run?** (a) Go, in `yardr-hosted/router`, as a third backend beside docker and droplet, the queue in a SQLite file of the router's: one edge, one map, one session check, built in the language and repository the edge already is, at the cost that it runs where the router runs, on the Mac behind the tunnel, and goes down with it. (b) TypeScript on Cloudflare Workers, a Durable Object per yard holding the socket and the queue: up when the Mac is not, nothing to keep running, at the cost of a second service that must check Kratos sessions and read the map the router owns, in a second language. (c) A separate Go service beside the router: separates the link's long-lived sockets from the router's requests, at the cost of the router forwarding to it for every connected row. I would take (a) now, with the frame protocol of 3.2 written so (b) can take over when the edge leaves the Mac.
2. **What does the yard do with the sender's timestamp on a delivery that waited at the edge?** (a) Check it against the edge-signed `received` time for a queued frame: queued Linear and Slack deliveries verify, and the window guards the edge's door, at the cost of trusting the edge not to replay (it holds the payloads anyway). (b) Keep the yard's clock: queued Linear and Slack deliveries are refused when they waited past 1 or 5 minutes, and only GitHub-like sources benefit from the queue. (c) Queue nothing, answer 503 when the yard is away: no payloads at rest on the edge, at the cost of losing every GitHub delivery while the laptop sleeps. I would take (a).
3. **The hosted credential limit: what is on the list, and how is it checked?** (a) API keys only, for every kind, stated at sign-up and offered alone by the hosted yardmaster's relay: simplest to defend, at the cost of excluding subscription users that Anthropic's page appears to allow ("an end user … signing in … with their own Claude subscription, including where a platform hosts Claude Code", 8wxe 2.10). (b) API keys plus each plan whose terms we have read as allowing hosted automated use, kept in `credentials.yaml` with the date it was read, today's Claude subscription reading included: matches the strategy's wording, at the cost of a list someone keeps current, and a reading of "ordinary, individual usage" against agents that run all day. (c) No list, a statement only. I would take (a) for the first hosted strangers and move to (b) once Anthropic has confirmed the reading (8wxe's open question 3). Either way: a statement and a narrowed relay, never a probe of the key.
4. **Does a connected yard take secrets over the link?** (a) No: secrets and approvals are answered on the yard's own machine, and the edge never carries a credential of a connected yard, which is what lets 3.7 say so. (b) Yes for `GiveSecret`, as a hosted yard takes it from its router: footplate can hand a token from a phone, at the cost that the edge then carries credentials in memory, and the 3.7 promise narrows to "never stores". I would take (a).
5. **Who may connect a yard?** (a) The trial's invite list, as hosted sign-up: same gate, the edge carries only people mckean knows. (b) Any verified account: a connected yard costs the edge a socket and a bounded queue, not a container, so the reason for the invite list (strangers' code on a shared kernel) does not apply. I would take (b), with the queue bound as the cost limit.

## 6. The beads it would become

In order. E1, E3 and E5 are in the `yardr-hosted` depot; E2, E4, E6 and E7's yard half in `yardr`. Under open question 1 (b) the yardr-hosted beads change their place, not their protocol.

- **E1. The edge keeps a key and enrols a yard by code** (yardr-hosted). Goal: the edge has an ed25519 key of its own (`router/keys/edge.key`, made on first start, 0600); `POST /edge/enrol` takes a yard's signed public key and name and answers a code and the edge key; `/connect` (Kratos session required) takes the code, shows the yard's name and key, and on approval writes a `connected` row through `provision register` (backend `connected`, the key in a new column, state `ready`); the enrolling call then answers with the handle. Where: `router/internal/router`, `router/internal/edgemap`, `provision/provision`. Done when: a test enrols a stand-in key end to end and the row reads `connected`; a wrong or expired code, a reused code, and an unsigned call are each refused with a test. Depends on nothing.
- **E2. `yardr edge connect`, `status` and `disconnect`** (yardr). Goal: the commands of 3.5, the `edge` setting (URL, edge key, handle; checked like `hook_public_url`: https, or http for loopback in tests), `hook_public_url` set on connect and cleared on disconnect, `--json`. Where: `internal/cli/edge.go`, `internal/store/settings.go`, `internal/yardkey`. Done when: against a stand-in enrol server the command connects, writes both settings, refuses an unsigned or wrongly keyed answer, and `disconnect` clears both. Depends on E1's protocol, not its deployment (a stand-in suffices).
- **E3. The link endpoint and `connected` webhooks, live** (yardr-hosted). Goal: `GET /edge/link` upgraded to a WebSocket after the signature check of 3.2, the answer signed; one socket per row, newest wins; ping every 30 s, dead after 75 s; `serveHooks` for a `connected` row sends a signed `request` frame and returns the yard's response, 8 s deadline, 503 with `X-Edge-Refusal: away` when no socket (until E5). Where: `router/internal/router/router.go` and a new `router/internal/link`. Done when: a test yard client receives a delivery and its answer reaches the sender byte for byte; a second connect closes the first; an unsigned connect is refused. Depends on E1.
- **E4. The yard's link in `yardr server`** (yardr). Goal: when `edge` is set, the server dials and keeps the link as 3.2 says (backoff 1 s to 60 s with jitter, pings), checks the edge's answer and frame signatures, and serves each frame through the listener's mux in process with `RemoteAddr` `edge:0`, with or without `hook_addr`; `edge_link_up`/`edge_link_down` events; `yardr status` and `yardr doctor` show the link. Where: `internal/server/server.go` (build the mux once for both), a new package `internal/edgelink` (AGENTS.md updated), the WebSocket library added to go.mod (proposed `github.com/coder/websocket`, no dependencies of its own). Done when: tests against a stand-in edge show a hook delivered and verified by the yard's secret, a `GiveSecret` over the link refused as not loopback, reconnect after the stand-in drops the socket silently, and a frame with a bad edge signature refused. Depends on E2. **The first slice ends here** (3.11), proved on mckean's laptop yard.
- **E5. The edge queues deliveries for an away yard** (yardr-hosted). Goal: 3.4's queue: 202 with an id when away or slow, kept in the router's own SQLite file, sent oldest first on reconnect with `"queued": true`, deleted on `ack`, bounded at 1,000 or 7 days with the drop counted. Where: `router/internal/link`. Done when: a test sends three deliveries to an away yard, reconnects, and sees them delivered in order, deleted only after ack, and redelivered once when the ack is lost. Depends on E3.
- **E6. The yard checks a queued delivery's timestamp against the edge's time** (yardr). Goal: under open question 2 (a), `declared.go`'s window uses the frame's edge-signed `received` for a queued frame, and the yard acks after recording or refusing. Where: `internal/hooks/declared.go`, `internal/edgelink`. Done when: a Linear-shaped delivery queued for ten minutes verifies through the link and the same body sent straight to the listener ten minutes late is refused. Depends on E4, E5; dropped under (b) or (c).
- **E7. footplate through the link** (both depots, two small beads). Goal: the router's session half sends `/user/*` for a `connected` row down the socket (yardr-hosted); `yardr user device pair` counts an up link as a listener and defaults `--url` to the edge (yardr). Where: `router/internal/router/router.go`; `internal/cli/devicepair.go`. Done when: footplate in hosted mode, signed in as mckean, pairs with his laptop yard by code and reads its conversations from another network. Depends on E4.
- **E8. Hosted sign-up states the credential limit** (yardr-hosted). Goal: `provision/credentials.yaml` per open question 3; the page that offers a hosted yard shows it and requires the statement before provisioning; the hosted yardmaster's role offers only the listed methods at the relay's `choose`. Where: `router/internal/router/ready.go`, `provision/`, the hosted pack's yardmaster role. Done when: provisioning without the statement is refused with a test, and the relay offers only listed methods in a stand-in login. Independent of E1 to E7.
- **E9. docs/edge.md: what the edge reads and holds** (yardr). Goal: 3.7 and 3.8 as user documentation, linked from docs/peers.md and docs/users.md, and the connect page links it. Done when: the page lands with E4.

The Google mint contract of 3.8 lands with yardr-hosted-4dx0's own beads; the web view through the link (3.6) is a design of its own after E7.
