# A footplate that is not on the yard's machine: the person's calls on a listener, and one statement of who is calling

Design yardr-xpf8, proposed 2026-10-12. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. A user is a person of a yard, a name and an inbox; the operator is the user who runs the yard; a crew member is one of the yard's always-on agents, and the yardmaster is the one that coordinates; a bead is one durable piece of work; an ask is one question as data on a bead; a peer is another yard this one is linked with; a source is a service outside the yard, declared by one file. docs/glossary.md has all of yardr's words. footplate is the native window over yardr's socket in which a person reads mail and picks. In this document a **device** is one program of one person on one machine or phone that calls a yard from outside it, a **credential** is what it sends to be let in, and **the listener** is the TCP address the yard's server already opens for webhooks and for linked yards (the setting `hook_addr`).

## 1. The question

mckean, by mail from airy, 12 Oct 2026 18:40: "I like how footplate works, and perhaps we should think of a model to remotely connect footplate directly, this would mean authentication, perhaps we could address it as how would we create a mobile footplate. and how does kratos/oathkeeper/keeto/hydra fit in? how can we make use of them without requiring them?" So: by what model does a footplate that is not on the yard's machine, in the end one on a phone, talk to a yard directly; what does the yard have to know about who is calling; and how do Ory's four (Kratos, Oathkeeper, Keto, Hydra) fit that model so that a yard gains from them where they run and works without them where they do not? Taken as given: yardr stays one yard for one operator and a person is a user per yard; yardr states what it lets in and builds no proxy and no login server; where an edge exists, security at the edge is outside yardr; an integration is a pack and a setting, never a trick in the core for one product.

## 2. What exists today

### 2.1. footplate, and the calls it makes

footplate (depot `footplate`, github.com/yardr-dev/footplate) is a native window for macOS and Linux, written in Rust and drawn with gpui, the UI framework of the editor Zed. Its README names what it reads and writes, and `src/api.rs` "is the whole client": it opens a unix socket (`UnixStream::connect`) at `$YARDR_HOME/yard.sock`, else `~/.yardr/yard.sock` (`socket_path`), and writes its own few lines of HTTP/1.1. It reads `Head`, `Settings`, `Crew`, `Peers`, `GetUser`, `UserInbox`, `UserNotifications`, `UserAsks`, `Bead`, `Conversation`, the stream `GET /events?after=<seq>` and images by `GET /blobs/<hash>`; it writes `Mail`, `MailPeer`, `PutBlob`, `AnswerAsk`, `DeferAsk`, `DeclineAsk`, `AddNote`, `AckUserInbox` and `AckUserNotifications`. Who the window is for "is not typed" (`login_name` and `who` in `src/session.rs`): `$YARDR_ACTOR`, else the login name, when the yard has a user of that name, else the yard's operator. When nothing answers it says so and tries again every 3 seconds. `src/api.rs`, `src/session.rs`, `src/model.rs` and `src/draft.rs` import nothing of gpui: the client, the state and the drafts are plain Rust, and only `src/window.rs`, `src/input.rs`, `src/select.rs` and `src/taskbar.rs` draw.

### 2.2. The socket, and what opening it means

docs/api.md is the contract for "a program that is not yardr", and it says of itself: "This is no network API: the socket is a file on the machine the yard runs on." The socket's mode is 0600 in a home of mode 0700. "That is all the authentication there is: a process that can open the socket is trusted as every party of the yard, and a call that takes an `actor` takes it at its word." docs/users.md says the same of commands: "yardr does not authenticate anyone".

Two facts in the code decide what a network form of it must do.

- **The socket serves every method of the store, not the documented ones alone.** `handler` in internal/rpc/server.go registers each method of `store.API` by reflection under `POST /call/<Method>`. `Calls` in internal/rpc/api.go is the list of the 21 a client may rely on, held to docs/api.md by a test; the others (adding a user, allowing a peer, setting a source, moving a bead) answer on the same socket. docs/api.md: "The socket serves all of yardr's".
- **Some checks go by the process that called.** `ConnContext` in internal/rpc/server.go records the pid at the other end of the unix connection, and internal/store/caller.go (`paneCrew`, `paneSession`) finds whether that process runs in an agent's pane. That is how an operator's command is "refused from an agent's pane" whatever actor it names. A TCP connection has no such pid.

Of the 21 calls, six name whose inbox or queue is read or acknowledged (`name` of `GetUser`, `UserInbox`, `AckUserInbox`, `UserNotifications`, `AckUserNotifications`, `UserAsks`), six name who writes (`actor` of `Mail`, `MailPeer`, `AnswerAsk`, `DeferAsk`, `DeclineAsk`, `AddNote`), and `Conversation` names both parties. `AddAnswer` in internal/store/asks.go does not compare the actor with the person an ask on a local bead is addressed to. The stream carries every event of the yard, and a `mail` event carries the mail's text in `data.text` whoever it is for.

### 2.3. The yard already has a network listener, and it was kept away from the store

`serveHooks` in internal/server/server.go opens one TCP listener when `hook_addr` (or `YARDR_HOOK_ADDR`) has an address, and serves two handlers on it: `/hooks/`, the webhooks of sources, and `/peers/`, the requests of linked yards. Its comment draws the line this design has to speak to: "It is a separate server with only the receiver's handler, so a port opened to the internet never reaches the store API or BDP." Each handler authenticates what it takes in its own way: a hook by its source's signature or, for the source `token`, by `Authorization: Bearer <token>` compared in constant time with a secret file's content (internal/hooks/token.go); a peer by an ed25519 signature over the request, with the link found by the sender's key (docs/peers.md, "The requests"). docs/peers.md says what yardr does not do: "put TLS in front of the listener (a reverse proxy or a tunnel of your own) and give the `https://` address as the URL. yardr does not do TLS itself."

Two other commands listen, each a client of the socket in a process of its own: `yardr web serve` (docs/web.md), loopback only unless `--unsafe`, GET only, "no authentication yet", answering only to the host names `--host` gives; and `yardr bdp serve` (docs/bdp.md).

### 2.4. How a yard is reached from another machine today

- **The socket forwarded over ssh.** `ssh -N -L <local path>:<remote socket> <host>` puts the yard's socket under a local path, and a footplate started with `YARDR_HOME` on it is a window on that yard (design yardr-h039, section 3.1). Its trust is ssh's, and it is whole: "the key is full access to everything the socket serves". A dropped link is a dead window.
- **A linked yard.** The person runs footplate against a yard of their own on the machine they sit at, linked with the yard that works: mckean on airy, linked with the Mac. Mail crosses as `name@peer`, an ask addressed `--to mckean@airy` stands in airy's decisions and its pick comes home (design yardr-h039, built: docs/peers.md, "Questions for a person of another yard"), and design yardr-r6jh, at decide today on branch `yard/yardr-r6jh`, proposes that an ask for a user is also sent to each name a link trusts as that user. Its rule 4 is the line this design starts from: "Nothing that acts follows ... A person who wants to act in the other yard acts there: in its terminal, through its socket forwarded over ssh, or through its web view at an edge."
- **The web view on a private network.** `yardr web serve --unsafe --host <name>` shows the yard to a phone that can reach the machine, read-only. Design yardr-8wxe, decided on 12 Oct 2026 with the draft's pick on every open question, has the view started for one user (`--as <user>`) take that user's writes; its beads yardr-y6ty (mail and the conversation) and yardr-bvt3 (asks, and a bead that waits for a person) are in backlog behind the trial yardr-aj24. Its open question 4 chose the flag and left the other option, "a header the proxy sets, mapped to a user", as "the step for a team", noting that yardr would then trust a header and need "the rule for which addresses may send it".

### 2.5. What yardr has for saying who someone is in an outside source, and for a program of the operator's

docs/users.md, "The record": the record "is where more about a person goes later: who they are in an outside source (as `yardr crew identity` says for a crew member) and a user who comes from another yard. Neither is built." For a crew member it is built: `yardr crew identity yardmaster --source github --key 'yardr[bot]'` (docs/crew.md).

A declared source gains a capability by a key in its file (docs/integrations.md): `notify` names the call that tells a person (packs/pushover/pushover.yaml), and `limits` names "a command", run by yardr itself without a shell, whose answer says what a subscription still carries. Design yardr-76zs added `mint_command`, "a setting only the operator sets, any program". So "the operator names a program, and yardr asks it one question" exists twice.

### 2.6. Being told

A user with `notify` set is told on a phone of what lands in their inbox, through the `pushover` or `telegram` pack (docs/users.md, "Told outside the yard"): mail and the notifications of their subscriptions, one message for a burst, at most 20 an hour, "No link is sent". An ask is not in the inbox and is told on no phone; design yardr-r6jh proposes it (its section 3.7 and its bead 4).

### 2.7. What Ory's four do and need, as their documentation says (read on 12 Oct 2026, ory.com/docs)

- **Kratos** (Ory Identities) holds the identities and does "registration, login, password reset, social sign in, profile management, 2fa, and more". A browser that logs in gets a session cookie. A native app that logs in through the API flow gets a JSON answer with a `session_token`, which it sends as `Authorization: Bearer <token>`; `GET /sessions/whoami` with that header answers the session and its identity. The login page warns: "Never use API flows to implement Browser applications!" A person can list their sessions and revoke one, "to log out from their account on different devices or terminate sessions they don't recognize", and an administrator can revoke any. Kratos needs a database (SQLite, PostgreSQL, MySQL or CockroachDB) and a login interface someone else writes: "Ory Kratos is API-only; it doesn't have a UI".
- **Oathkeeper** "authorizes incoming HTTP requests", usually as "a reverse proxy in front of your upstream API or web server that rejects unauthorized requests and forwards authorized ones", in four steps for each request: match an access rule, authenticate, authorize, mutate. Its `cookie_session` and `bearer_token` authenticators "forward the request method, path and headers to a session store" (Kratos's `/sessions/whoami`) and take the subject from its answer; `jwt` verifies a token against key sets it fetches; `oauth2_introspection` asks an OAuth2 server whether a token is active. Its mutators decide what the upstream sees: `noop` "doesn't transform the HTTP request and simply forwards the headers as-is"; `header` adds headers such as `X-User: {{ print .Subject }}`; `id_token` puts a short-lived signed token (one minute by default) in `Authorization`, whose public key the upstream reads from Oathkeeper's `/.well-known/jwks.json`. It "doesn't have any dependencies to other services ... and doesn't require a database".
- **Hydra** (Ory OAuth2) is an OAuth2 and OpenID Connect server. It does not log anyone in: it needs a database and a login and consent application, which Kratos can be. Ory's page on when to use it says it is for letting "other companies and developers ... access the data of your users with their consent", for machine-to-machine tokens and for devices such as televisions, and that "If you are building a new mobile app or single-page app backed by an API, then using a traditional 'Login' solution" is the better choice. It supports the device authorization grant (RFC 8628): a device shows a code, the person confirms it in a browser elsewhere, the device polls for its tokens.
- **Keto** (Ory Permissions) answers whether a subject has a relation to an object, on the model of Google's Zanzibar: "`User:x is in readers of Document:y`". Oathkeeper's authorizers (`remote_json`, and one for Keto's older policies) can ask such a service for each request.

## 3. The proposal

### 3.1. The shapes, side by side

| | A. A yard of its own on the device, linked | B. The socket over ssh | C. The person's calls on the listener, with a credential the yard checks itself | D. The same listener, with someone else saying who calls | E. The web view as the mobile footplate |
|---|---|---|---|---|---|
| Exists | yes (airy and the Mac) | yes | no | no | read-only today; acting is yardr-y6ty and yardr-bvt3 |
| On a phone | no: a yard is yardr's server, a store and a daemon, and no phone runs them | no, short of a terminal application | yes | yes | yes, in the browser, with no client to write |
| What the device may do | what the link carries: mail, and asks addressed across or, with yardr-r6jh, copied | everything the socket serves, as any party | the 21 calls of docs/api.md, as one user | the same | what the view offers, as the user it was started for |
| Who says who is calling | each yard's operator, per link and name | ssh | the yard: a token the operator gave to one device of one user | an identity service the operator named | whoever started the view (`--as`), for every request alike; an edge decides who reaches it |
| Taking it back | `peer rm`, `--trust` taken back | the ssh key removed | the token removed, one device at a time | the session ended at the identity service, or the mapping removed | the edge's rules |
| When the yard is not reachable | both directions queue for a day | a dead window | a client that says so and tries again | the same | a page that does not load |
| Costs | a second yard to run and keep; two names for one person; nothing that acts follows | all or nothing; a terminal | one handler on the listener, a record per device; the line of 2.3 is moved, on purpose and narrowly | C, and one more capability of a source | one user per view; the browser's limits (no push of its own, no key store) |

The proposal (open question 1) is that **C and D are one model and the others are steps or neighbours**. The model in one sentence: the yard serves the calls a person's client may make, and only those, on its listener, to a caller whose request carries a credential that the yard resolves to one user of this yard, and every such call is made as that user. C and D differ only in who resolves the credential. A is not a way to a phone, and stays what it is for: a person who works in two yards. B stays the way that needs nothing built and gives everything. E is the mobile footplate that needs no client, it is decided already, and it is the first thing a phone shows; it meets this model where it says who a request is (3.5).

### 3.2. The statement of identity

What the yard needs from outside is one statement for each request:

> this request is user X of this yard, said by S, where S is something this yard's operator named.

On the wire the credential is one header, `Authorization: Bearer <credential>`, the form Kratos gives a native app, the form an OAuth2 token has, and the form yardr's own `token` hook already takes. A credential of the second kind below comes with one header more, `Yardr-Identity-Source: <source>`, which names the one source that is asked about it (3.5). The yard resolves the credential to a pair (user, said by) or refuses with 401, the same answer whatever the reason, as a peer's request is refused. S is one of two kinds:

1. **A device of the yard's own** (3.4): the credential is a token the yard made, and the yard holds its hash. S is the device's name: `phone`.
2. **A source the operator gave the capability `identity`** (3.5): the yard asks the source the request names, and no other, who the bearer is, and the operator has said which subject of that source is which user. S is the source's name: `kratos`.

Nothing else says who a request is. In particular no header a proxy adds is believed (`X-User`, `X-Forwarded-User`): a header is true only when nothing but the proxy can reach the listener, which is a fact about a network that yardr cannot see, and it is the "rule for which addresses may send it" that design yardr-8wxe named as that option's cost. A credential the yard resolves itself needs no such rule, and a proxy in front loses nothing by it (3.5). This is open question 3's option C, not taken.

A statement never makes a user. A subject that maps to no user of the yard is refused; users are added by the operator, as today.

### 3.3. The listener serves the person's calls, and nothing else of the store

A third handler on the listener of 2.3, beside `/hooks/` and `/peers/` (open question 2 has the other places):

```text
POST <url>/user/call/<Method>     one of the 21 calls of docs/api.md, arguments as a JSON object
GET  <url>/user/events?after=<seq>
GET  <url>/user/blobs/<hash>
GET  <url>/user/me                {"user": "mckean", "said_by": "phone", "yard": "mac"}
```

It is docs/api.md over TCP with one header more, so that a client changes its transport and nothing else. What makes it safe to stand where the store was kept away from:

- **Only the calls of `Calls`.** Any other method is answered as no such method, and so is the array form of the arguments. A test lists every method of `store.API` and holds that the handler serves exactly the 21. No operator's command, no move of a bead, no setting and no secret is reachable through it, whoever calls.
- **Every call is made as the resolved user.** A `name` or `actor` that is left out is filled in with the user; one that names anyone else is refused. One side of `Conversation` must be the user. So a device cannot write as a crew member or as another user, and cannot acknowledge another's inbox.
- **No credential, no answer.** Without a valid one every path answers 401, `/user/me` too.
- **A refusal costs the yard one hash, and costs nobody else anything.** The yard does not slow or refuse by the address a request comes from. Behind a reverse proxy, which this model puts in front (2.3, and the TLS rule below), every caller has the proxy's address, so a limit for each address would let one caller's wrong credentials shut out every device behind that proxy; and a header that names the true address (`X-Forwarded-For`) is not read, for the reason the web view gives: "whoever asks writes it" (`streamKey` in internal/web/events.go). Slowing an address is the edge's, which sees the address. Guessing needs no limit: a token is 256 random bits. What the yard does itself is keep a refusal cheap and keep nothing for it: the credential is hashed and looked up before the request's body is read or anything of the yard is, and refusals are counted by their reason and logged in sums, at most one event for a reason in ten minutes, as the hook receiver's are (`refuse` in internal/hooks/receiver.go). No count is kept for an address or for a credential that did not resolve, so there is nothing a stranger can fill and no answer a stranger can turn against a device. An identity source's command is the one costly step a wrong credential could reach, and 3.5 bounds it.
- **The handler is there only when someone can use it.** With no device and no identity source the path answers 404, as the pairing's paths do while no pairing is open. A yard that never gives a credential has the listener it has today.
- **A write says how it came.** The event of a write made through the handler carries `via` in its data (`"via": "phone"`), and the views that show the writer show it: `mckean (via phone)`. Whether an agent sees that mark is open question 5.
- **Streams are bounded** for each credential, as the web view bounds them for each address (docs/web.md, "How many").

Reading is not narrowed to the user. A device reads what the socket's 21 calls and the stream give: every bead with its notes, the crew, the users, and through the stream the text of all mail in the yard. That is what a user of the yard can read today with `yardr inbox --actor` and what the web view shows of beads, and it fits one yard for one operator. It is said here because it is what a lost credential gives away (3.7), and whether a user other than the operator gets a device is open question 4.

The check that goes by the calling process (2.2) has nothing to check here, and needs nothing: every command it guards is outside the 21.

TLS is not yardr's, as for peers: a reverse proxy or a tunnel of the operator's stands in front, and the listener cannot tell whether one does. So the rule sits in the client, where the address is known: footplate refuses a yard's address that is `http://` unless its host is loopback (3.6).

### 3.4. A device token: what a yard with nothing else uses

```text
$ yardr user device add mckean phone
device phone of mckean. Its token, shown once:

  yardr-dev-7Kq2...<43 characters>

Give it to the device with this yard's address.
It calls as mckean until: yardr user device rm mckean phone

$ yardr user device list
USER    DEVICE  ADDED        LAST USED
mckean  phone   10-12 19:02  10-12 19:40 from 100.64.0.7
mckean  airy    10-12 19:05  never

$ yardr user device rm mckean phone
device phone of mckean removed: its token is refused from now on, and 1 stream it held was closed
```

- The token is 32 random bytes; the store keeps its SHA-256 and the device's name, user, when it was added and when and from where it was last used, never the token. It is compared in constant time.
- Adding one is the operator's, under the check `yardr user add` uses, and refused from an agent's pane as `user set notify=` is (docs/users.md, "Who may"): whoever adds a device decides who may speak as a person. Removing one is the operator's and the user's own.
- The yard does not know the address it is reached at from outside (docs/integrations.md, gap 9), so the command takes it as `--url` to print it, as `yardr peer invite --url` does, and without one says to give the device the address it will reach.
- `device_added`, `device_removed` are events; `yardr user show`, `yardr doctor` and `yardr status` list the devices, and doctor notes one not used for 30 days. Whether a token ends by itself is open question 6.

This is the whole of "without requiring them": a yard with no identity service gives each device a token and takes it back by name.

### 3.5. An identity source: how Ory is used where it runs

A source's file gains one key, on the model of `limits`: **a command** that is given a credential and answers who it is.

```yaml
name: kratos
about: Who a Kratos session token belongs to.
identity:
  command: [/usr/local/bin/yardr-kratos-whoami, "https://id.example.net"]   # the bearer on stdin; prints {"subject": "<identity id>", "until": "<time>"} or exits 1
```

```text
$ yardr source add kratos.yaml
$ yardr user identity mckean --source kratos --key 9f3c1a52-...        # the operator: this subject there is mckean here
$ yardr source check kratos                                            # reads a token from the terminal, without echo, and says whom it resolves to
```

`yardr user identity` is what docs/users.md says the record is for and what `yardr crew identity` is for a crew member. **The request names the one source that is asked.** A credential that is no device token is a secret of the service that issued it, and the yard cannot tell from an opaque token which service that is. So the caller says, in a header beside the credential, as a peer's request names its key in `Yardr-Peer-Key` for the receiver to find the link by (docs/peers.md, "The requests"):

```text
Authorization: Bearer <the session token>
Yardr-Identity-Source: kratos
```

The name is looked up among the sources that have the key `identity`, and the credential is given to that source's command and to no other. There is no default and no second try: a request without the header, with the header twice, or with a name that is not such a source is 401 and runs nothing, also in a yard that has one identity source only; and when the named source's command refuses the credential, fails or does not answer, the request is 401 and no other source is asked. So a credential reaches a service that did not issue it only when the caller itself names that service.

The header chooses who is asked and says nothing about who is calling. The user comes from the subject that the named source's command answers and the operator's mapping of that subject in that source (`yardr user identity ... --source kratos`), so a subject of one source is never looked up in another's mapping, and "said by" is the source that was named and answered, one always. The name is text from outside, held to the form a source's name has (`validName` in internal/source/declaration.go) and compared with a fixed list; it is no secret, and the device is given it with the yard's address.

An answer is kept for 60 seconds under the source's name and the credential's hash together, so a session ended at the service is refused within a minute, the command runs once a minute for each device and not once for each call, and an answer kept for one source never answers a request that names another. A subject mapped to nobody is 401.

The command is a program run for a caller the yard does not know yet, so what a stranger can make of it is bounded, again without counting by address (3.3). A device token is known by its form (`yardr-dev-`) and is looked up in the store and never given to a command: no number of wrong bearers delays a device. A refusal is kept for 60 seconds under the same pair as an answer is, in a table of fixed size, so one wrong bearer sent again for the same source runs nothing. At most four commands run at a time, and they are counted in two lots: two for a credential that resolved to a user through the named source within the last day, two for every other. A bearer that finds its lot full is answered 503 with `Retry-After` and waits for nothing. So a flood of bearers never seen can delay a person's first request of a new login, and nothing else: not a device, and not a person already let in, whose credential is asked about again in the lot a stranger cannot enter. Where an edge stands in front (Oathkeeper below), the flood ends there. yardr holds no logic of any identity product: the command is the operator's program, as the limits command is, and a pack `ory` brings the file above and a small program that asks Kratos's `/sessions/whoami`.

What each of the four supplies under this model:

| | supplies | used by the model? | how |
|---|---|---|---|
| **Kratos** | the person's login (password, social sign-in, a second factor, recovery), a session token for a native app, and its end: a session listed and revoked for each device | yes; it is the one that says who | the phone's footplate logs in with Kratos's API flow and sends the session token as its bearer, with the source's name beside it; the yard's identity command of that source asks `/sessions/whoami`; the operator maps the identity's id to a user. A lost phone's session is revoked in Kratos, by the person from another device |
| **Oathkeeper** | the edge: it drops a request without a valid session before it reaches the yard, lets through only the paths its rules name, as a reverse proxy in front of the listener | yes, as a second line in front, not as the one that says who | a rule for the paths under `/user/` with the `bearer_token` authenticator on Kratos's whoami and the `noop` mutator, which passes `Authorization` and `Yardr-Identity-Source` on as they came; the yard resolves the same token again, through the source the request names. Its `header` mutator is not believed (3.2). Its `id_token` mutator would do: a signed token is a credential an identity command can verify against Oathkeeper's key set, in a source of its own whose name the device is given; no pack brings that program here |
| **Hydra** | OAuth2 and OpenID Connect: tokens for an application that is not the yard's own, given with the person's consent, and the device grant | not for footplate, on Ory's own advice (2.7). It fits unchanged when wanted | an access token is a bearer; an identity command asks Hydra's token introspection whether it is active and whose it is. The cases: someone else's application reading a person's yard with their consent, or logging a terminal footplate in by a code shown on another screen |
| **Keto** | the answer to "may this subject do this to that object" | no | inside one yard the only question is "which user is this", and what a user may do is the yard's own: the operator setting, a group's members. Keto's place is a service's edge (design yardr-8wxe): which account reaches which customer's yard, asked by Oathkeeper's authorizer, outside yardr |

A yard with none of them uses device tokens. A yard with Kratos alone uses the identity source and needs no Oathkeeper. A yard behind both gains the edge's drop of strangers and loses nothing if a rule of the edge is wrong, because the yard still resolves the credential itself; that is the answer to design yardr-8wxe's row "A mistake in the proxy's rules is a breach", for this path.

The web view of design yardr-8wxe is the same statement in its simplest form: user X, said by whoever started the view, for every request alike. Nothing here changes it. A view that resolved a credential for each request (the browser's Kratos cookie, through the same identity command) would be that design's "step for a team"; it is not proposed here (section 4).

### 3.6. What footplate changes, and what a phone's footplate is

**The window that exists.** footplate gets a second way to its yard: an address and a token in place of a socket.

```text
$ FOOTPLATE_YARD=https://mac.example.net FOOTPLATE_TOKEN_FILE=~/.config/footplate/mac.token footplate
```

`src/api.rs` gains a transport beside `UnixStream`: TCP with TLS, the same requests under `/user/`, the bearer on each. It asks `/user/me` for who it is, in place of `$YARDR_ACTOR`. The token is read from a file of mode 0600 (the rule of every env file in yardr) or, on macOS, the Keychain; it is never in the settings file. An `http://` address off loopback is refused with the reason. A 401 is a state of its own, "this device is no longer let in", and is not tried again every 3 seconds. Everything above the client is unchanged. With this, mckean's laptop footplate is a window on the Mac's yard with no link, no ssh and no second yard, and beside r6jh's joined asks rather than in place of them: the link carries what is said to him, the listener is where he acts in the Mac's yard.

**A phone.** gpui draws on macOS and Linux (the README builds for both and for nothing else), so the window does not go to a phone. A phone's footplate is a second front on the same client: `api.rs`, `session.rs`, `model.rs` and `draft.rs` are plain Rust (2.1) and can be a library that a phone's interface is built over. Which interface (a native one for each system, or one toolkit for both) is a design question of the footplate depot and is not answered here (open question 7, and bead F2). Until one exists the phone's footplate is the web view (shape E).

### 3.7. What a phone needs beyond a connection

- **Being told.** It exists and is independent of all of the above: `notify` through the `pushover` or `telegram` pack tells of mail, and design yardr-r6jh's bead 4 adds an ask. A phone's footplate needs no push channel of its own for a first version: the push comes through the pack's application, and the person opens footplate. A push that opens footplate on the question (a link in the message, which the Pushover declaration has a parameter for and yardr does not fill) is left out here (section 4).
- **A yard that cannot be reached from outside.** A device calls the yard; the yard cannot ask a phone what waits, as a yard behind a router asks its peer (docs/peers.md, "Push and pull"). So the yard's listener must be reachable by the device, and how is the operator's network, not yardr's (the decision of 9 Oct 2026, kept on yardr-nxah: "yardr defines what to allow; it does not handle Tailscale or proxies"): a private network both are on, or a tunnel or reverse proxy of the operator's to `hook_addr`. Where neither is wanted, what exists composes: a small yard that can be reached and has no agents, linked with the one that works, with design yardr-r6jh's join bringing the person's asks to it, and the phone calling that yard's listener. Open question 8.
- **A device that is lost.** `yardr user device rm mckean phone` on the yard, from a terminal or by asking the operator; with Kratos, the session revoked from any other device. The token is refused at the next request, and its open streams are closed. What the finder had until then is in the table below.

### 3.8. Care

| what | what stands against it |
|---|---|
| A stolen credential reads the yard | It reads everything a user can: beads, notes, the mail of everyone in the yard, images. Nothing stands against it but the removal, `last used` with its address in `device list`, and for an identity source the service's own second factor and session ages. A device token is a bearer token and no more than that |
| A stolen credential writes as the person | Mail as them to any crew member and to the names linked yards expose, answers to asks, notes on beads. The yardmaster does what the operator asks by mail, so this is the power to have work shaped, built and landed. The mark `via <device>` on each write says how it came (open question 5); the removal ends it |
| Acting as a user from outside becomes acting as the operator | The handler serves the 21 calls and no other method, held by a test (3.3). The operator's commands are not among them, and a crew member asked by mail cannot run them either: they are refused from an agent's pane. So what a remote user can cause is what the crew may do, and never `peer allow`, `user add`, `user device add`, `source set`, `crew set` or `hook add` |
| A remote caller names another actor | The resolved user is the actor and the name of every call; anything else is refused (3.3) |
| Text from outside is taken as instructions | What a device writes is its user's word, as it is from the terminal, and is shown with `via`. Nothing the handler takes is run or used as a path: a method's name is looked up in a fixed list, a device's name is held to the characters a user's name has, a credential is hashed. What an identity command answers is data: a subject compared with the operator's mapping, never a user's name of its own choosing, so a service that lies about a subject can only name subjects the operator mapped |
| The credential is read on the way | TLS in front, the operator's; footplate refuses plain http off loopback (3.6). yardr cannot check it from the inside and says so in the documentation, as docs/peers.md does |
| Guessing | 256 random bits: no limit on tries is needed against it, and the yard has none |
| A flood of wrong credentials, to shut others out or to wear the yard down | The yard keeps no count for an address, so there is no limit a stranger can use up for the devices that share a proxy's address with them (3.3). A refusal is one hash and one lookup; a device token never waits on an identity command, and a person already let in has a lot of the command's runs that a stranger's bearers cannot take (3.5). What is left is the load of the requests themselves, which is the edge's to slow, since only it sees the address they come from |
| The identity service is down or wrong | Down: its credentials are refused within the minute an answer is kept, device tokens go on working. Wrong, or its operator hostile: it can let anyone in as any mapped user, which is what naming it means; map only subjects of a service you run or would trust with that user's word, the rule docs/peers.md gives for a trusted name |
| A credential is shown to a service that did not issue it | A device token is given to no command (3.5). Any other bearer is given to the one source its request names, and to no other when that source refuses it, fails or is not there: no source is tried in turn. A caller that names the wrong source has shown its credential there itself; the yard adds no showing of its own. The name chooses who is asked and makes nobody a user: that is the subject the source answers, in that source's mapping alone |
| The listener is opened to the internet for the first time because of this | It is the listener peers and webhooks already use and may be open already. A yard that opens it only for devices now also answers `/hooks/` and `/peers/` there; both refuse what is not signed or not theirs. docs says to pass only `/user/` through a proxy where that is all that is wanted |
| A secret typed into footplate (design bead yardr-0qs5) crosses the network | Not designed here. That design gets this as input: over the listener a value travels under the operator's TLS and no other protection |

### 3.9. A first step, with one phone

It needs the two yardr beads Y1 and Y2 and the footplate bead F1 below, and no identity service.

1. On the Mac: `yardr user device add mckean airy` and `yardr user device add mckean phone`. The listener is reachable on the private network the three machines share; a reverse proxy of mckean's gives it TLS.
2. On the laptop: footplate with `FOOTPLATE_YARD` and the first token, for a week of real use beside the link with airy. This is the remote footplate.
3. On the phone, with no application written: one saved request (an iOS Shortcut's "Get Contents of URL", or `curl` in a terminal application) that posts `/user/call/UserAsks` with the second token and shows the open questions, and a second that answers one. `notify` through Pushover is set, so the phone is told of mail.
4. `yardr user device rm mckean phone`, and the saved request is refused.

What it answers, as a note on the trial's bead: whether a direct window is what he uses once it exists, or the linked yard stays the place; what the phone lacked most (a real interface, being told of an ask, reaching the yard away from the private network), which orders open questions 7 and 8 and the beads after the trial; and whether a token with no end felt right after a week.

## 4. Left out

- **A login in yardr**: passwords, sessions, a login page, a second factor. "yardr states what it lets in"; a device token is a credential the operator hands over, not a login.
- **A header a proxy sets, believed by address** (3.2), and **verifying signed tokens in yardr's core** (a key set fetched and checked by yardr itself). The first rests on a network yardr cannot see. The second is a product's protocol in the core where a command does; open question 3 keeps both as options.
- **The whole socket on the network**, or a second list of calls for remote use. The 21 calls are the contract; a call a remote person needs later is added to docs/api.md for every client.
- **Moving a bead from footplate** (what `yardr review` does). No call for it is in docs/api.md today; design yardr-8wxe's bead yardr-bvt3 gives it to the web view. When it becomes a documented call, the listener serves it as any other.
- **Narrowing what a device reads to its user's own**: a stream filtered for each user, mail hidden between users of one yard. It would be the first wall between users of a yard, and design yardr-8wxe left "a team in one yard" out for the same reason.
- **The web view resolving a credential for each request** (the step for a team of design yardr-8wxe), and one listener for the view and the calls. The view keeps `--as`.
- **Enrolment by a code or a picture** (a QR code the phone reads, a pairing as two yards pair). A token pasted once is enough for a first step; the pairing of docs/peers.md is the model if it is wanted.
- **A relay of yardr's own** for a yard that cannot be reached, and yardr setting up a tunnel or a tailnet.
- **A push channel of footplate's own** (Apple's or Google's push service), and a link in the push that opens footplate.
- **Slowing or refusing by address in the yard** (an answer 429 after so many refusals). Behind a proxy it is one caller's way to shut out the others (3.3), and believing a forwarded address is the proxy's header again. The edge slows addresses; the yard keeps a refusal cheap.
- **Trying a bearer with each identity source in turn**, a source that is asked when the request names none, and **telling the source from the credential's form** (a prefix declared in the source's file, as a device token is known by `yardr-dev-`). The first two show a credential to a service that did not issue it, and with two sources leave open which one said who is calling. The third fails where two services issue credentials of one form, as two issuers of signed tokens do. **The source's name inside the bearer** (`kratos:<token>`) is left out because an edge passes `Authorization` on to the identity service as it came (2.7), which would then be shown a credential it does not know.
- **Scopes on a token** (read only, asks only). One kind of device, one set of calls; a read-only window on a phone is the web view.
- **The phone's interface itself.** A design of the footplate depot.

## 5. Open questions

**1. Which shape is the model?** Option A, the listener with a statement of identity (shapes C and D as one), as proposed: footplate anywhere is a window on the yard that works, and Ory fits as one kind of "said by"; it costs a handler that reaches 21 store calls from the network, a record for each device, and the upkeep of a second way in. Option B, the web view only (shape E): nothing but yardr-y6ty and yardr-bvt3, which are decided; a phone and a laptop both use a browser, footplate stays a window for the yard's own machine or an ssh forward, and Ory fits only as the edge. Option C, the linked yard only (shape A with design yardr-r6jh): nothing new; no phone, and nothing that acts follows. I would take A, with E as the phone's first screen and the order of section 6.

**2. Where does the handler live?** Option A, on the listener that exists, under `/user/`, as proposed: one address, one TLS front and one thing to keep running, and it runs in the server, so it can close a removed device's streams at once. It costs the line "a port opened to the internet never reaches the store API": after this it reaches 21 calls of it, behind a credential, and a fault in the handler is a fault on a port that may face the internet. Option B, a command of its own, `yardr user serve --addr`, a client of the socket as `yardr web serve` is: the port is opened only by whoever starts it and the server's listener stays as it is; it costs one more long-running command with its start, stop, install and status, and a process that itself holds the whole socket. Option C, inside `yardr web serve`: one port for people, the view's Host check and stream limits at hand; it costs tying a program's API to a browser's view, which runs the build it was started on until restarted and is loopback unless `--unsafe`. I would take A.

**3. In what form may something outside say who a caller is?** Option A, a source's `identity` command, as proposed: the request names the source, the operator's program answers a subject, the operator maps subjects to users, and a pack brings the program for Kratos; it costs a command run once a minute for each active device, a minute's delay on a revocation, and a second header that a device must be told to send. Option B, yardr verifies signed tokens itself against an issuer and key set the operator names: no program to install and no call for each token; it costs token parsing and key fetching in the core, and it fits Oathkeeper's `id_token` and Hydra but not a Kratos session token, which is opaque. Option C, a header from a proxy at an address the operator names: the least code and what Oathkeeper's `header` mutator gives; it is true only while nothing else reaches the listener. Option D, none yet: device tokens alone until a yard stands behind an identity service, with this section kept as the plan. I would take A, built after the first step has been tried (bead Y3), and would not take C.

**4. Who gets a device?** Option A, any user of the yard, each reading what any user can (3.3), as proposed: a second person of the yard gets a window too, and their device reads the operator's mail in the stream, as their terminal could. Option B, the operator only, until a yard has walls between its users: nothing new is readable by anyone, and a second person stays on the machine or the web view. I would take A, since a user of a yard can read all of this today, and say it in `device add`'s output for a user who is not the operator.

**5. Does an agent see how a write came?** Option A, the mark `via <device>` is in the event and in what people see (`yardr chat`, footplate, the web view), and an agent reads the mail as the user's with no mark, as proposed: no agent behaves differently for a person on a phone. Option B, agents see it too (`mail from mckean (via phone)` in the inbox and the prime), and the yardmaster's role says nothing about it: the record is in front of the one who acts on it. Option C, as B, and the starter pack's yardmaster role gets a line: what comes `via` a device is the person's word for work, and a request to change who may reach the yard is answered with "do that at the yard's terminal". I would take B: the mark costs nothing to show, and a role line is a rule an agent may or may not keep.

**6. Does a device token end by itself?** Option A, never, until removed, with `last used` shown and doctor's note at 30 days unused, as proposed: nothing stops working by surprise. Option B, it ends after 90 days and is given again: a forgotten token dies; it costs a phone that stops one morning and a person who must reach the yard's terminal to go on. Option C, it ends after 30 days without use: forgotten tokens die and used ones live; it costs a rule to explain. I would take A for the first step and decide again after the trial, which asks exactly this.

**7. Is a native phone footplate built?** Option A, not yet: the web view is the phone's footplate, and after the trial a design bead in the footplate depot says what a native one would be and cost (bead F2), as proposed. Option B, yes, as soon as the listener exists: a real interface in the hand sooner, and two fronts to keep in step while the first is still changing weekly. Option C, never: the browser is enough, and footplate is for desks. I would take A.

**8. What does yardr say to a yard that cannot be reached from outside?** Option A, it documents the operator's ways (a private network both are on, a tunnel or proxy to `hook_addr`) and the composition with a small reachable yard and design yardr-r6jh's join, and builds nothing, as proposed. Option B, a relay of yardr's own that a yard holds a connection open to: a phone reaches any yard; it is a service to run and to trust with every call. I would take A.

## 6. The beads it would become

In the order they would land. Y1 and Y2 are in the depot yardr and depend on open questions 1, 2 and 4 (and Y2 on 5); F1 is in the depot footplate. Each yardr bead passes `sh .yardr/test` and updates the pages named; anything that starts a server is tried in an isolated yard with a listener on `127.0.0.1:0`.

**Y1. A user's devices: `yardr user device add`, `list` and `rm`.** Goal: section 3.4 without the listener: the record, the token shown once, the events. Where to look: the user record and `user set` in internal/store (the `notify` and `env_file` fields) and internal/cli for the commands; `mayTell` in internal/store/usernotify.go for the check that refuses an agent's pane; internal/hooks/token.go for the constant-time comparison; `yardr user show`, `yardr doctor` and `yardr status` for where devices are listed. Done when: `device add` prints a token once and the store holds only its SHA-256; a second device of the same name for the same user is refused; adding is refused for anyone but the operator and from an agent's pane, removing for anyone but the operator and the user; `device list`, `user show` and doctor print what 3.4 shows, with doctor's note for 30 days unused; `user rm` removes the user's devices; a function of the store resolves a token to (user, device) or to nothing and records the last use; docs/users.md has a section "Devices". Depends on nothing built.

**Y2. The listener serves the person's calls to a device.** Goal: section 3.3. Where to look: `serveHooks` in internal/server/server.go for the mux and its comment, which is rewritten to say what the port reaches now; `handler`, `invoke`, `named` and `Calls` in internal/rpc for the calls, the stream and the blobs, to be served from one implementation and not copied; the pairing handlers' conditional registration in internal/yards/receiver.go for "there only when someone can use it"; the web view's stream limits in internal/web; `addNoted` and the `mail` event for `via`. Done when: with a device token the 21 calls, the stream and a blob answer under `/user/` as docs/api.md says they do on the socket, held by running that page's test through the handler; every other method of `store.API`, and the array form, is refused, held by a test that lists the API; a `name` or `actor` that is not the device's user is refused and one left out is filled in; without a credential, or with a removed one, every path is 401, and with no device in the yard 404; a refusal is answered before the body is read, is counted by its reason and logged in sums as the hook receiver's are, and keeps nothing for the address: a test sends a thousand wrong credentials from one address and a device at that same address is answered as before, and no request is answered 429; removing a device closes its streams; a write's event carries `via`, shown as open question 5 was answered; docs/api.md gains "Over the network" with the paths, the header, `/user/me`, the 401 and what footplate must do about TLS, and docs/users.md says what a device reads and writes. Depends on Y1.

**F1 (footplate). The window opens a yard by address and token.** Goal: section 3.6, the window that exists. Where to look: `Client`, `call_once`, `events` and `blob` in src/api.rs, which open a `UnixStream` each; `login_name` and `who` in src/session.rs; `Trouble` for the new state; src/fake.rs for a fake yard on TCP; the README's "What it talks to". Done when: with `FOOTPLATE_YARD` and a token file of mode 0600 the window shows the yard at that address as the user `/user/me` names, and without them it opens the socket as today; an `http://` address off loopback and a token file open to others are refused with the reason; a 401 shows "this device is no longer let in" and is not retried; the tests run the client against the fake yard over both transports; the README says all of it. Depends on Y2.

**T. The trial of 3.9.** mckean's, with the yardmaster keeping the record: a week of the laptop's window, the phone's two saved requests, a removal, and the note of what was found. Depends on F1.

**Y3. A source says who a bearer is, and a user has an identity in it.** Goal: section 3.5. Only if open question 3 is answered with A. Where to look: the `limits` key and its command in internal/source and internal/limits for the declaration, the run without a shell and `source check`; `yardr crew identity` in internal/cli and internal/hooks/identity.go for the mapping's shape; Y1's resolver, which gains the second kind; packs/pushover for a pack of kind source with its SETUP.md. Done when: a source file with `identity.command` is accepted and `source check` resolves a token typed without echo; `yardr user identity <user> --source <s> --key <subject>` is the operator's, one subject one user; a bearer that is no device token is resolved through the command of the source that `Yardr-Identity-Source` names and through no other, kept for 60 seconds under the source and the credential's hash, refused when the command fails or the subject maps to nobody in that source's mapping; without the header, with it twice or with a name that is no identity source the request is 401 and no command runs, also with one identity source in the yard; a test with two identity sources A and B, each a stand-in command that records what it was given, holds that a bearer sent for A is never given to B: when A resolves it, when A refuses it, when A exits with an error, when A does not return, and when it is sent again within the 60 seconds, and that an answer kept for A does not answer the same bearer sent for B; a subject that both sources answer, mapped to a user in B only, is refused when the request names A; a device token is never given to a command, whatever the header says; a refusal is kept as an answer is, in a table of fixed size; the commands run in the two lots of 3.5, held by a test in which a stand-in command that does not return is run for bearers never seen until their lot is full, the next such bearer is answered 503, and a credential that resolved before and a device token are both still answered; `/user/me` and `via` name the source that was named and answered; docs/api.md's "Over the network" gains the header; `packs/ory` brings the file and a program that asks Kratos's `/sessions/whoami`, tested against a stand-in for Kratos on `127.0.0.1:0`, and its SETUP.md has the Oathkeeper rule of 3.5 as an example yardr tests no further; docs/integrations.md has the capability in its table. Depends on Y2.

**F2 (footplate). Design: a footplate on a phone.** A design bead in the footplate depot, after T: the client as a library, which interface, how it logs in (a pasted device token, or Kratos's API flow where Y3 exists, with the source's name it then sends), where the credential is kept, what it shows first. Depends on T and on open question 7.

Beads that exist and are not made here: yardr-y6ty and yardr-bvt3 (the web view acts) stay where design yardr-8wxe put them, and are the phone's first footplate; design yardr-r6jh's bead 4 (an ask is told on the phone) is the part of 3.7 a phone gains most from; design bead yardr-0qs5 (a secret) gets the last row of 3.8 as input.
