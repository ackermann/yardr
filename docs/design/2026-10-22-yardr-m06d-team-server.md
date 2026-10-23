# A team server: one internal box, each person's Claude, footplate per person

Design yardr-m06d, proposed 2026-10-22. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. A
depot is a git repository or a plain directory beads are worked in; a bead is
one durable piece of work. A group is who works a stage of a flow (agents,
a script, or people), and a route sends a stage's beads to a group. The
operator is the one person the `operator` setting names; a user is a person
of the yard, with a name and an inbox. A crew member is an always-on agent
tied to no bead; the yardmaster is the crew member that coordinates. A
harness is the agent program herdr starts (here only `claude`), and a login
is that program's own sign-in, kept in a folder. footplate is the desktop
app a person talks to the yard with; a device is one such program of one
user, let in by a token. A source is an outside service declared by one
file, which the yard calls on an agent's behalf. `docs/glossary.md` has the
rest. Paths are relative to the repository root at commit fbd9764f unless
another repository is named; footplate was read at its commit f2e0eb7.

## 1. The question

What does a team need to run its own yard on an internal server, and what
does each gap cost? This design lays out the setup and the remaining work:
one yard on one internal box, with each person using their own Claude login
and connecting through footplate or the web view. The gaps, one by one:
Claude-only configuration, who may do what, Windows, network, the team's
sources, data and compliance, and installation and upgrades by the team's
IT operator. Implementation, pricing, the licence decision (yardr-avjm) and
a demo yard are left out.

## 2. What exists today

### 2.1. People, inboxes and devices

- **Users.** `User` in `internal/store/users.go` (name, mail and
  notification cursors, subscriptions, the source that tells them, source
  identities); `yardr user add|list|show|rm|set|key|identity` in
  `internal/cli/users.go`; docs/users.md. Only the operator adds or removes
  a user (`AddUser`, `DeleteUser` behind `operatorOnly`, users.go).
- **Inboxes.** `UserInbox`, `UserNotifications`, `Conversation`
  (users.go): mail and notifications per user, read in `yardr inbox`,
  `yardr chat`, the web view and footplate.
- **Devices.** `yardr user device add|pair|list|rm`
  (`internal/cli/devices.go`, `internal/cli/devicepair.go`; store side
  `internal/store/devices.go`, `devicepair.go`). A device holds a token
  (`yardr-dev-` and 32 random bytes; the store keeps its SHA-256). Pairing
  is an eight-character code valid ten minutes, redeemed with `POST
  <address>/user/pair`. Every call of the device is made as its user:
  `asUser` and `selfArg` in `internal/rpc/user.go` force the actor to the
  token's user.
- **What a device may call** is a fixed list, `Calls` in
  `internal/rpc/api.go`: reads (users, crew, beads, flows, arrivals,
  conversations, inboxes) and a person's writes (mail, reactions, notes,
  answering, deferring, declining and taking back asks). There is no bead
  create, no move and no setting on the listener. `OperatorCalls`
  (`GiveSecret`, `ApproveAsk`, `ApproveSetup`, `SourceGrants`) are served
  to a device of the operator alone, except `GiveSecret` for a user's own
  secret ask (`UserServer.ownSecret`).

### 2.2. Whose subscription builds the work

Design yardr-2k0x landed:

- **The originator.** `Bead.Originator` (`internal/store/beads.go`) is the
  user a bead is for; `yardr bead create --for <user>`
  (`internal/cli/bead_edit.go`); defaults in `originatorFor` (beads.go).
- **Personal groups and routes.** `login_for=<user>` on a herdr group
  (`internal/store/configkeys.go`, `Group.LoginFor` in
  `internal/store/routing.go`) declares whose login its `env_file` carries
  (`CLAUDE_CONFIG_DIR=<folder>`, the `home_env` of `harnesses/claude.yaml`).
  A route with `--originator <user>` takes that person's beads.
  docs/groups.md, "Whose login a group carries".
- **The led login.** `yardr harness login claude --group <group> --json`
  (`internal/cli/harnesslogin.go`) runs the harness's own sign-in into that
  group's folder. For a person not at the box, the yardmaster relays it: the
  sign-in URL goes to the person, and the code they paste back is a
  login-input secret ask whose value goes only to the waiting login
  (`internal/store/loginwait.go`, `internal/store/secretask.go`; design
  yardr-bwm2 §3.5).
- **A conversation per person.** `yardr crew set yardmaster for=<users>
  env_for_<user>=<env file>` runs one yardmaster session per served user,
  on that user's login (`EnvForPrefix`, `Serves`, `EnvFor` in
  `internal/store/crew.go`; docs/crew.md). That session talks with the
  person and files beads for them with `--for`.

A prior design decision (mem-wcn7, 14 Oct 2026) reads Anthropic's terms as
allowing the yard's agents to run on a person's own consumer subscription,
with the known risk that Consumer Terms §2 and §3(7) could be read the other
way.

### 2.3. Grants, the operator, and what is checked

- **The operator** is the `operator` setting (`OperatorSetting`,
  `operatorIn`, `notOperator`, `operatorOnly` in `internal/store/users.go`).
  Every operator gate compares the call's actor with that one name, inside
  the store's write. The comment on `notOperator` says it plainly: "a line
  against mistakes, not a wall, and moves when yardr hosts more than one
  person".
- **Who is acting on the socket** is the caller's word: `--actor`, else
  `$YARDR_ACTOR`, else the login name (`config.ActorWith`,
  `internal/config/config.go`). The socket tells only what kind of caller it
  is, by its process tree (`internal/peer`, `internal/store/caller.go`):
  a bead's agent, a crew pane, the daemon, or a person. The class table in
  `internal/rpc/access.go` (read, agent, admin, operator, daemon) is by
  caller kind, not by user. docs/users.md, "Who is acting": "whoever can run
  yardr on the yard's machine can name any actor".
- **Two doors do authenticate.** A device token (above), and the web view in
  JWT mode: `web_jwt_issuer`, `web_jwt_audience`, `web_jwt_jwks`,
  `web_jwt_header`, `web_jwt_users` (`internal/web/jwt.go`; docs/web.md,
  "Behind an auth gateway", with rows for Cloudflare Access, oauth2-proxy
  and Pomerium). Each visitor then acts as their own user.
- **Source grants** go to a group or a crew member, never to a user
  (`Grantee`, `SourceGrant`, `grantKey` in `internal/store/sourcecalls.go`).
  A plain grant is the operator's (`maySource`, `internal/store/sources.go`);
  a grant `--as <user>`, which calls with that user's own credential
  (`~/.yardr/users/<user>.env`, design yardr-erzr), is that user's alone.
  `yardr source grant|revoke|grants` in `internal/cli/sourcecmd.go` and
  `sourcegrants.go`.
- **Asks.** A run ask is approved by the operator, or by its addressee for
  their own command (`mayApprove`, `mayApproveOwn`, `mayApproveSole` in
  `internal/store/runask.go`). A stage ask is answered only by the stage
  group's members (`checkMembers`, `internal/store/routing.go`). A plain
  ask's answer is checked for its shape only (`answerable`,
  `internal/store/asks.go`): nothing checks that the answerer is the user it
  was put to, except the doors (a device answers as its user; the web shows
  a visitor only the asks put to them).
- **Visibility.** None by user: `ListBeads`, `Bead`, `Notes`, `Events` are
  read calls for anyone; docs/users.md says a device "reads every bead". Mail
  is the one thing filtered per user (`deviceEvent` in `rpc/user.go`).
- **Manual groups.** `people: true` in a pack, or `--runner manual
  --members a,b`; `yardr review` (`internal/cli/review.go`, docs/review.md)
  and stage asks (`## Ask` in a stage file, docs/flows.md) put a person's
  yes in the flow.

### 2.4. Harnesses

`harnesses/` holds claude, codex, grok, kimi, kiro, muse, opencode and pi;
`agents.DefaultKind` is `claude` (`internal/agents/harness.go`). No shipped
pack names a kind or a model (`packs/starter/pack.yaml`: "It names no agent
product, model, depot, person or secret"); a group's kind is answered at
import by `--bind <group>=<kind>[:<model>]` or the `default_kind` setting
(`internal/cli/pack.go`, `internal/pack/apply.go`). The brakeman and the
shaper are this yard's own crew and stages, not in any pack. The Go code
naming other kinds runs only when a group uses that kind: `codexTrustNotes`
(`internal/cli/codextrust.go`) and `kimiHookNotes`
(`internal/cli/kimihooks.go`) return nothing otherwise, and `yardr doctor`
asks only for the kinds in use (`harnessUses`, `internal/cli/needs.go`).

### 2.5. Network

- **One listener**, `hook_addr`, serving `/hooks/`, `/peers/` and `/user/`
  (`serveHooks` in `internal/server/server.go`); unset, the yard listens
  nowhere. `hook_public_url` binds nothing. `web_addr`, `web_pages_addr`
  and `bdp_addr` are loopback unless `--unsafe` (docs/web.md "Behind a
  proxy", docs/configuration.md "Settings").
- **No TLS in yardr.** docs/api.md, "TLS is yours, and the client's rule":
  a reverse proxy or tunnel stands in front, and a client refuses `http://`
  except to loopback. `GiveSecret`, `ApproveAsk`, `ApproveSetup` and
  `SourceGrants` are taken only from a request that reached the listener
  from the yard's own machine (`inTheClear` and the loopback checks in
  `internal/rpc/user.go`), and never from one whose proxy says
  `X-Forwarded-Proto: http`. So the proxy that ends TLS must run on the
  yard's box.
- **Proxies.** The decision of 5 Oct 2026 (mem-stdz; designs yardr-7mkc,
  yardr-xpf8 question 8, yardr-hosted-050n §3.10): yardr says what it
  allows and builds no proxy. Outbound, Go's clients honour `HTTPS_PROXY`
  (`http.ProxyFromEnvironment` in `internal/source/call.go`, the default
  client elsewhere), but the variables never reach the long-running
  processes: `server.ServerEnv` (`internal/server/server.go`) passes only
  `PATH`, `HOME` and `YARDR_*`, and `serviceEnv` (`internal/cli/service.go`)
  adds only `HERDR_SOCKET_PATH`, `XDG_CONFIG_HOME` and the inbox retention.
  An agent's pane gets them only through a group's or crew member's
  `env_file`.
- **What a yard calls out to**: each harness (claude to Anthropic), git
  remotes, the sources it declares, the edge if connected, a JWKS URL and an
  OTLP endpoint if set. yardr has no update check. Plain `yardr doctor`
  fetches nothing; `yardr doctor --try` makes one real call per harness
  login and checks the configured limits commands and sources, so it
  reaches Anthropic and each source.

### 2.6. footplate

footplate (`README.md`, `src/api.rs` in the footplate repository) reaches a
yard two ways: the local unix socket, or **paired by address**: TLS
(rustls, system roots) to `<address>/user/`, the device token in a 0600
file. A paired footplate needs no yardr, herdr or claude on the laptop: the
agents run on the yard's box. Released for macOS and Linux only
(`scripts/release.sh`). Unix-only code: the socket in `src/api.rs`, file
modes and inode checks in `src/yard.rs` and `src/fetch.rs`, the config
folder in `src/settings.rs`; notifications (`src/notify.rs`) and the unlock
(`src/unlock.rs`) already fall back on other systems (no notices; type the
ask's number back). gpui, its UI library, has a Windows platform. yardr-ens6
and footplate-6qt6 (Windows Hello) hold the Windows plans.

### 2.7. Sources and non-code work

- **The Google packs** (`packs/google-mail`, `google-drive`, `google-docs`,
  `google-calendar`) are the model of a reading and writing source: a
  declaration file (`base`, a credential with a `mint_command`, `calls`
  each `read` or `update`), presets of grants, a `SETUP.md`; the mint
  script `google-mint` runs on the box. docs/google.md;
  docs/integrations.md, "A declared source" and "Adding a service". The
  format carries JSON over HTTPS only: no paging, no upload, no download of
  a file's bytes (docs/integrations.md, "What is not there").
- **Directory depots** (`DepotDir`, `IsDir` in `internal/store/graph.go`;
  `startDir` in `internal/runner/herdr.go`; `packs/dir`;
  docs/dir-depots.md): a plain folder, worked in place, no branch, a
  person reviewing the outputs.
- Nothing in the repository mentions SharePoint, Microsoft Graph,
  Confluence or a document management system.

### 2.8. Data

The home (`~/.yardr`, `internal/config/config.go`; table in
docs/configuration.md): `yard.db` (bbolt: beads, notes, mail, asks,
events, kept webhook bodies), `blobs/` (mail files, `blob_max_bytes` 8 MiB
each), `server.log`, `daemon.log`, `web.log`, `logs/sessions/` (never
pruned), `worktrees/<depot>/<bead>`, `backups/`, credentials in
`sources/`, `groups/`, `crew/`, `users/`, `harness/`. Each person's Claude
login sits in the folder their group's env names, beside Claude Code's own
transcripts of every session (`projects/*/<session>.jsonl`,
`harnesses/claude.yaml`). Retention (docs/configuration.md): events and
ended sessions 90 days (`YARDR_EVENT_RETENTION`), webhook bodies 30 days,
an unread inbox holds the log at most 180 days; these are environment
variables, not settings. Beads and notes are never pruned. `yardr user rm`
keeps the person's mail in the log; there is no command that erases a bead
or a person's data, short of `yardr uninstall --purge`. Backups
(`backup_every`, `backup_keep`, `backup_dir`; `yardr store backup`) copy
`yard.db` only.

### 2.9. Install and upgrade

- docs/install.md, "On a server (no desktop)", and
  `contrib/server-yard/server-yard --harness <kind> --operator <name>`:
  herdr as a systemd user unit, `loginctl enable-linger`, `yardr init`,
  `yardr daemon install`, `yardr web install`, `yardr doctor`. It installs
  no program and logs no harness in; its README says it has not yet run on
  a fresh Linux server.
- Release archives for linux and darwin, amd64 and arm64
  (`contrib/release/build`), on the private repository: downloading needs
  a GitHub account that can read it (docs/install.md).
- `yardr upgrade` builds from a source checkout with Go (`upgrade_from`,
  `internal/cli/upgrade*.go`, docs/upgrade.md). An archive install upgrades
  by replacing the binary; the server and daemon re-exec themselves on a
  newer build (`internal/version`).
- `yardr doctor` (`internal/cli/doctor.go`, docs/doctor.md) checks the
  programs, the server, daemon and crew, the setup steps, the listener,
  the sources and the login services. `yardr setup` walks the same steps as
  forms.

### 2.10. The hosted service

`yardr-hosted` (its README; designs yardr-bwm2, yardr-hosted-050n,
mem-0khv): sign-up at my.yardr.dev through Kratos, a provisioner that
makes **one whole yard per person** (a container on the Mac now, a droplet
later), a router that checks the session and proxies to that person's yard,
minting the JWT the web view verifies. The image is Debian with pinned
yardr, herdr and claude, running as the user `yard` on a home volume, with
an entrypoint that runs the yard without systemd. A yard connected to the
edge takes webhooks and footplate's calls over an outbound link
(`internal/edgelink`, docs/edge.md); the edge sees payloads and device
tokens in the clear. The hosted service isolates people by machine, with
one yard per person. A team server instead has one yard shared by its
members.

## 3. The proposal

One sentence: a team server is one Linux box on the team's own network
running one yard on Claude alone, its people reaching it through footplate
paired by address or through the web view behind the team's login, each
person's work and conversation running on their own Claude login in a
folder on that box, with the team's documents reaching the agents as a
folder; and before the first team starts, six small beads (B1 to B6) make
that box installable and reachable by the team's IT operator. Roles,
erasure, a Windows footplate and a Microsoft pack follow, in that order,
each sized below.

### 3.1. Claude only: configuration, nothing to build

Nothing in the code needs muse, kimi or codex (§2.4). A Claude-only yard is
`yardr init --harness claude`, which makes `default_kind` claude and the
yardmaster a claude session; `yardr depot init <depot>` with the `dir`
pack, whose agent groups are answered `claude` by default; and each
person's group made with `--set kind=claude`. What this yard runs on Muse
(the brakeman, the shaper, the train stages of `.yardr/flows`) is this
depot's own way of working and does not travel with any pack.

Two things are worth a line in the operator's page (§3.7) and no bead: the
claude harness's model is chosen per group by `--bind
builders=claude:<model>`, so a team that has only some models chooses
there; and `yardr doctor --try` makes one real claude call per login, which
is the check that the box can reach Anthropic at all.

**Size: zero beads.** One paragraph in the page of §3.7.

### 3.2. Who may do what

**What it would replace.** Today one name, the `operator` setting, holds
every admin right, and every other user holds the rights of a person on
their own things (their inbox, their devices, their grants `--as` them,
their run asks). Beyond that, a person sees everything.

**Where a role can mean anything.** On the socket a role is a guardrail only:
anyone with a shell on the box names any actor (§2.3). On a team server the
people do not have a shell on the box; they come through the two doors that
authenticate, a device token and the web view's JWT. So the model is
enforced in the store, as every check is today, and is a wall exactly where
identity is proved: for people who reach the yard only by footplate or the
web view. The operator, and whoever else has a shell on the box, stays
trusted, as the design of yardr-2k0x already says of the login folders.

**What a door can carry today.** The doors are narrow on purpose. A device
may call `Calls` and, if it is the operator's, `OperatorCalls`
(`internal/rpc/api.go`): approving a run ask or a setup ask, giving a secret,
and reading the grant graph. Nothing that administers the yard is there: no
user add, no device pairing, no depot, no source, no grant, no setting
(`SourceGrants` only reads). And `operatorCall` in `internal/rpc/user.go`
serves `OperatorCalls` to a device whose user equals the one `operator`
setting. So administration happens at the box's shell, by the IT person who
installed it; what a remote person can be given is the right to approve.

**The model, small enough to start with.** A role is a field on the user,
one of three. The table says what each role may do through a door; the
shell column is what the same role is trusted with on the box, where the
store's check is a guardrail against mistakes and not a wall.

| Role | Through footplate or the web view | At the box's shell | Replaces |
|---|---|---|---|
| `operator` | the `OperatorCalls`: approve any run ask and setup ask, give a secret, read the grants; plus everything a member may | everything the operator setting allows today: add users, devices, depots, sources, grants, settings | the single `operator` setting, which becomes "the first operator" for what needs one name (the fallback addressee of an ask) |
| `member` | talk with the crew; have beads filed for them; answer asks put to them and stage asks of groups they are in; approve run asks for their own login and give their own secrets (today's `mayApproveOwn`, `ownSecret`); see beads of the depots they belong to | (a member has no shell) | what every user is today, narrowed by depot |
| `reader` | see the beads of the depots they belong to, and their own mail; answer nothing | (none) | nothing today; the person who only watches |

And one list on a depot: `members=<users>` (unset: everyone, as today).
A member or reader sees a bead when they belong to its depot or are its
originator; the operator sees all. That rule holds for what the doors show
a person directly: the bead lists, the arrivals board, a bead's page and the
event stream of footplate and the web view.

**What the roles do not guarantee.** They do not keep one person's work
from another through the agents. The crew is shared: the yardmaster that
talks with Bea is the same crew member, with the same grants and the same
socket, that talks with Ada, and it can read any bead and mail what it read
into Bea's conversation. A filter in the yardmaster's role text ("tell a
person only of their depots") is an instruction, not a check. Code can go
one step further: the store knows which person a conversation pane serves
(`paneCrewSession` in `internal/store/caller.go`, the same knowledge the
source binding below uses), so the read calls from that pane could be
filtered by that person's visibility, at one bead more. It is still not a
wall, because every agent runs as the yard's one OS user with a shell:
it can open the worktrees of every depot, the backups of `yard.db` under
`<home>/backups`, and every person's login folder (§3.6). Confidentiality
between the team's people, against the agents that serve them, needs an
OS user per person, which §4 leaves out. So the model offers one
guarantee, stated as such in the page of §3.7: a person who reaches the
yard only through a door sees through that door only what the rule
allows. Whether the team's people may see each other's work at all is for
the team to say (§5, question 2); if they may not, the answer is separate
yards, one per set of people who must not see each other's work, not roles
in one yard.

Administration through a door (adding a person or pairing their laptop
from the web view, say) is not in this model: it would be new calls on the
listener, each a write the doors have so far been kept from, and a team
server has an operator at the shell. Whether a remote operator needs any of
it is §5, question 9.

What four examples of who may do what come to:

- **Approve run asks**: operators any; a member only their own (today's
  `mayApproveOwn`). `operatorCall` serves `OperatorCalls` to "a device of a
  user whose role is operator" instead of "a device of the user the
  `operator` setting names", and the store's `mayApprove` asks the role in
  the same way.
- **Add a depot**: an operator, at the shell (today `PutDepot` is an admin
  call, open to any person and any crew member on the socket; the check
  moves into the store, where on the socket it is a guardrail).
- **See whose beads**: the depot list plus originator, above.
- **Read which sources**: not a role, but one binding that is missing
  today, below.

**Sources: whose credential a conversation may use.** A source read as the
person, with their own credential (`--as`, §2.3), is held by the source's
own permissions, which already say who reads which SharePoint site; yardr
does not copy them. But yardr must make sure the person whose
credential is used is the person being served, and today it does not. The
grant's holder is a group or a crew member (`sourceCaller.grantee` in
`internal/store/sourcecalls.go`), `may` checks the grant of that holder for
the user `as` names, and `callables` lists every grant `--as` any user that
holder has. So `yardr source grant <source> <call> --crew yardmaster --as
ada` lets every conversation of the yardmaster, Bea's and Carl's too, call
as Ada. The store already knows which conversation a pane is:
`paneCrewSession` (`internal/store/caller.go`) returns the crew member and
the served user, and notes and handoffs are tagged with it. The binding is:

- a call from a crew member's conversation pane for user X may use a grant
  `--as X` and no other user's; the line session (the crew member's own,
  serving nobody) and a crew call told only by the actor may use no grant
  `--as` a user once that crew member keeps conversations;
- a call from a bead's session may use a grant `--as X` only when X is the
  bead's originator, so a group shared by several people cannot read with
  one person's credential for another's bead;
- `callables` lists, for such a caller, only the grants it may use, so the
  agent is not shown calls it would be refused.

That is one bead, in `sourceCallerIn`, `may` and `callables`, with rows in
the existing source call tests. A per-user source pack (§3.5) depends on it.
The alternative is a deliberately shared service identity: one plain grant
of a service account that IT scopes to what every member of the team may
read, which needs no binding because no one's own credential is used (§5,
question 5).

Plus two holes the doors leave open today, closed in the same work as the
roles: a plain ask's answer is taken only from the user it was put to or an
operator (`answerable` checks nothing of the answerer), and `UserInbox`,
`AckUserInbox` are refused for another user's name except to an operator.

**The boundary, for a module apart.** This layer may become the paid part
(yardr-avjm). The seam that makes that possible is one function the store
asks: `may(ctx, tx, actor, action, object) error`, with `action` one of a
fixed list (approve a run ask, add a user, add a depot, see a bead, answer
an ask, …) and `object` the bead, depot or user it touches. The core's
implementation is today's rules: one operator, everyone sees everything. A
module's implementation reads roles and depot members. Visibility is the
same function asked at the doors (`rpc/user.go`, `internal/web`) and in the
read calls a device makes.

Where the module's code can live is set by Go itself. Every package is under
`internal/`, and Go refuses to compile an import of
`yardr.dev/yardr/internal/...` from any other module (AGENTS.md: nothing
outside `internal/` imports it except `main` and the tests). `docs/api.md`
is a contract for programs talking over the socket, not for Go code linked
into the binary. So "another repository imports the store and calls
`store.SetPolicy`" does not build as the code stands. Three boundaries do:

- **Same module, a build tag.** The module's code sits in this repository
  under its own directory and licence file (`internal/roles/`, every file
  `//go:build roles`), registered from `main` when built with `-tags roles`.
  The release builds two binaries from one tree. The policy needs no
  public API and no versioning of its own: it changes with the store in the
  same commit, and the gate builds and tests both. The licence boundary is a
  directory and a tag, not a repository, which yardr-avjm has to accept.
  Cost: 1 bead on top of the seam.
- **Another repository, through public packages.** Two packages leave
  `internal/`: `yardr.dev/yardr/policy` (the `Policy` interface, the list of
  actions, `Register`) and a public entry point that runs the CLI with a
  policy installed. The store imports `policy`, not the reverse. The
  module's own `go.mod` requires `yardr.dev/yardr` at an exact version;
  `policy` carries a version constant that `Register` checks, refusing a
  module built against another one, so an action added to the list is a
  version change the module must follow. The core then keeps a Go API
  stable besides `docs/api.md`, and a release builds the module's binary
  against each core release. Cost: 2 beads on top of the seam (the public
  packages and the version check; the second repository and its release),
  and a standing cost at every change to the action list.
- **Out of process.** The store asks an operator-configured command, as a
  source's `mint_command` is asked, for each decision. No Go coupling, but a
  process per check, and visibility filters every read call: too slow for
  the read path, and refused here.

Either way:

- Every gate in §2.3 routes through the one function: some 140 uses
  of `operatorOnly`, `maySource`, `mayApprove*`, `mayTell`, `checkMembers`
  and friends, a refactor with no change of behaviour, worth having even if
  nothing is ever sold.
- The CLI-only gates move into the store (`app.operatorOnly` in
  `internal/cli/peers.go` for edge connect, `upgrade --from`, pairing,
  uninstall), since a module cannot hold a check the store never asks.
- Read calls gain a viewer: `Bead`, `ListBeads`, `Arrivals`, the event
  stream and the web lists filter by `may(see)`. The store passes the
  viewer down; today they take none.
- Roles are a stored field and depot members a stored list either way; the
  core migrates them, so a yard's data reads the same under either binary.

**A core binary on a restricted yard fails closed.** A core binary that read
the stored restrictions and ignored them would show every bead to everyone
the moment a yard ran without the module, by a slip at upgrade as easily as
by choice: `yardr upgrade` builds with `go install .`
(`internal/cli/upgrade_install.go`), with no build tag, so a roles yard that
upgrades the usual way would come back as the core. So the store records,
in the write that first sets a restriction (a role other than `operator`
or `member`, or a depot's `members`), the setting `policy_required=roles`.
A binary whose policy is not `roles` and finds that setting serves no door:
the listener's device calls and the web view answer 503 with "this yard's
restrictions need the roles build", and `yardr doctor` reports it as a
fault. The shell, which the model trusts anyway, keeps working, so the
operator can mend it either way: install the roles binary again, or take
the restrictions off deliberately (`yardr depot set <depot> members=` for
each depot, roles back to `member`) and then `yardr settings unset
policy_required`, which the store refuses while a restriction remains.
`yardr upgrade` on the roles binary builds with `-tags roles` (or the
module's entry point), so the usual upgrade keeps the module. Leaving the
module is thus a deliberate act at the shell, and the doors are closed
until it is done. Cost: part of the module's bead (B14) and of depot
members (B10, which writes the setting); no bead of its own.

**The smallest version for a first team** is narrower than the model:
several operators (the role field with two values, `operator` and `member`,
and `operatorCall` reading it), the answerer check on plain asks, and the
inbox check. For an initial team, everyone seeing every bead may be
acceptable; depot members and `reader` wait for the answer to §5, question
2. The source binding is needed only once a source reads as a person
(§3.5), which a synced folder does not.

**Size:** the smallest version, 2 beads (roles with two values and the
operator moves; the answerer and inbox checks). The source binding, 1 bead,
before a per-user source pack. The seam refactor, 2 beads. Depot members and
visibility at the doors, 2 beads. The module apart, 1 bead by a build tag or
2 by another repository (§5, question 3). Eight or nine in all, two before
a first team starts.

### 3.3. Windows

**What exists.** A paired footplate needs nothing local but itself (§2.6),
and the web view in JWT mode gives a person in a browser their conversation,
their asks and their beads (docs/web.md, "Talk", "The right column").

**The smallest Windows footplate** is paired by address only, with no
local yard:

- the socket way behind `#[cfg(unix)]` in `src/api.rs`, `HOME` falling back
  to `USERPROFILE`;
- the private-file check and the 0600 write of `src/yard.rs` behind
  `#[cfg(unix)]`, with the token kept in the Windows credential store
  instead (the client rule of docs/api.md allows "the system's key store");
- `src/fetch.rs` and the set-up-here path of `src/setup.rs` compiled out,
  so the first screen offers pairing alone;
- the settings folder under `%APPDATA%` in `src/settings.rs`;
- notifications and the unlock as today's fallbacks (none; type the
  number back), Windows toasts and Windows Hello (footplate-6qt6) later;
- a build: `cargo-xwin` on the Mac (a few GB, which yardr-ens6 parked for
  want of disk), or a Windows runner, with GitHub Actions off on these
  repositories.

The yard itself stays on Linux: yardr does not build for Windows (yardr-ens6
counts sixteen files of Unix calls), and nothing in a team server asks it to.

**The smallest version** is no new code: people on Windows use the web view
through the team's login gateway, and footplate where a laptop is a Mac or
Linux. What a Windows person then lacks is an operator's approval (run asks
and setup asks are approved on a device or the socket, not the web view),
which matters only if an operator sits at Windows.

**Size:** 0 beads to start. A paired-only Windows footplate, 3 beads in
footplate (the cfg split and key store; the first screen; the build and
release), after disk or a runner is found.

### 3.4. Network: what the operator configures

IT gives a name (say `yard.internal.example`), a certificate for it, and, if
the box reaches the internet only through one, an egress proxy and its CA.
The operator then sets:

1. **The listener on loopback**: `yardr settings set hook_addr
   127.0.0.1:8790`, then `yardr server stop` so the next command restarts it
   listening.
2. **A reverse proxy on the same box**, ending TLS with IT's certificate
   and passing `https://yard.internal.example/user/` to `127.0.0.1:8790`, and
   nothing else of it (`/hooks/` and `/peers/` stay closed unless a webhook
   source needs one). It must run on the box itself: the approvals and
   secrets of §2.5 are refused from anywhere else. Throttling, if any, is
   the proxy's (yardr never answers 429).
3. **The web view behind the team's login**: an auth gateway that puts a
   signed ID token in a header (oauth2-proxy against the team's identity
   provider is the row of docs/web.md that fits an Entra or other OIDC
   provider), then the five `web_jwt_*` settings, one `email=user` pair per
   person in `web_jwt_users`, and `yardr web install --host
   yard.internal.example`. The view stays on loopback; the gateway runs on
   the box.
4. **The pages origin**, which serves the files a run attaches or makes
   (`//yard.internal.example:8789/blobs/<hash>`) from an origin apart from the
   view's, so a file cannot act as the person: the proxy forwards that port
   too, to where the origin listens, with the Host header unchanged
   (docs/web.md, the paragraph on a proxy in front of the view). Where IT
   would rather open no second port, it gives a second name instead
   (`pages.yard.internal.example`, with its certificate) and the operator adds
   `--pages-host pages.yard.internal.example` to `web install`. Without one of
   the two, a web user cannot open an attached or generated file.
5. **Pairing**: `yardr user device pair <user> laptop --url
   https://yard.internal.example`, the code given to the person, who types it
   into footplate.
6. **Egress**, where the box has a proxy: `HTTPS_PROXY`, `NO_PROXY` and,
   for a proxy that inspects TLS, `NODE_EXTRA_CA_CERTS` (claude runs on
   Node, which does not read the system's store) in each person's group env
   file and the yardmaster's; the proxy's CA in the system store for yardr's
   own calls, which Go on Linux reads. The server and daemon do not get the
   proxy variables today (§2.5); one bead passes them through (below).
7. **Nothing to the edge.** A team server does not run `yardr edge
   connect`: the edge is yardr's hosted service, outside the team's network,
   and sees what crosses it.

This stays inside the decision of 5 Oct: yardr builds no proxy and terminates
no TLS. What is new is passing the standard proxy and CA variables to the
processes it starts, which is configuration the operator already gives.

**Size:** 1 bead (pass `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`,
`SSL_CERT_FILE`, `SSL_CERT_DIR` and `NODE_EXTRA_CA_CERTS` through
`server.ServerEnv`, `serviceEnv` and the herdr unit `yardr up --login`
writes). The steps above go into the page of §3.7.

### 3.5. The team's sources

The first use this design takes is knowledge work: agents read a body of
documents, answer and summarise, and write a result a person checks. What a
team needs first is that the agents can **read the documents**, not that
they can search an API: a person asks the yardmaster for a summary of a
folder, a bead is filed for them, an agent reads the files and writes its
answer next to them, and the person reviews it in a manual stage.

That shape exists: a directory depot (§2.7) over a folder of the documents.
The smallest way the team's documents become such a folder is a sync IT
already allows, run by the operator outside yardr, on a timer: `rclone
sync` of one SharePoint library or OneDrive folder, or a mounted file
share. No pack and no source; the service's own permissions decide what
that one folder holds.

That folder is one corpus for everyone. The sync reads with one identity,
so it holds what that identity may read, and a directory depot has no
per-person access: every agent of the yard, serving any person, reads all
of it (and §3.2 says why roles do not change that). So the folder holds
only documents every member of the team may read. Where access differs
between people, the folder route does not fit: either one folder per group
of people allowed the same documents, each its own depot and its own yard
when they must not see each other (§5, question 2, (d)), or a source that
reads as each person (below).

`rclone sync` makes its destination match the library, so it deletes
whatever else it finds there, an agent's answer included. The depot's
folder therefore has two parts that never overlap:

```text
/srv/yard/knowledge/            the directory depot (yardr depot add knowledge /srv/yard/knowledge --kind dir)
  library/                      rclone sync's destination: the documents, replaced at each run
  answers/                      the agents' outputs: no sync writes or deletes here
```

The sync names `library/` and nothing above it (`rclone sync
sharepoint:Knowledge /srv/yard/knowledge/library`), and the depot's stage file
says outputs go under `answers/` and `library/` is read, never written. An
agent that writes into `library/` anyway loses that file at the next run,
which is the sync working, not a loss of the yard's work. Where IT wants
that enforced rather than asked, the sync runs as another OS user that owns
`library/`, and the yard's user gets read access only; with a mounted file
share, the mount is read-only. A share that is itself where answers belong
is mounted beside `answers/`, never on top of it.

A pack comes after, judged by where the team's documents live:

- **Microsoft Graph** (SharePoint, OneDrive, Outlook, Teams behind one
  sign-in): the likeliest for a team on Microsoft 365, and the largest.
  Search and listing fit the declared-source format; reading a Word file's
  text does not, since the format downloads no file's bytes (§2.7). It needs
  that gap closed (a call whose answer is a file written into the bead's
  worktree) and a mint against the team's identity provider.
- **Confluence**: its REST API answers page bodies as JSON, which the format
  carries as it is; a read pack is the size of `google-docs`.
- **A document management system**: unknown until named; often no public
  API, and the folder route is then the only one.

Either pack reads under one of two identities. As each person (`--as`,
with their own credential), the service's permissions decide what each
reads, and the source binding of §3.2 must land first, or one person's
conversation reads with another's credential. As one service account (a
plain grant), IT scopes that account to what every member of the team may
read, and nothing more is built. Which one is §5, question 5.

**Size:** 0 beads to start (a section of the page of §3.7 on the synced
folder). Confluence read: 2 beads. Microsoft Graph read: 4 beads
(file bytes into a worktree in the source format; the pack's declaration
and mint; `SETUP.md` with the app registration; a rehearsal against a
stand-in). Read as each person: 1 bead more, the binding of §3.2.

### 3.6. Data and compliance: the facts a review asks for

Which rules bind a team's use of the yard is for the team to apply; this
design gives no advice on them. These are the facts a compliance review
asks for, as the code stands, for a page the operator hands over (§3.7):

**What leaves the box.**

- To Anthropic, through each person's Claude login: every prompt, every
  file an agent reads, every answer; under the terms of the plan that login
  belongs to (§5, question 4). Claude Code also sends its own telemetry and
  error reports, which its own settings turn off; this design did not
  verify their names.
- To each declared source: the calls the agents make, logged as
  `source_call` events.
- To git remotes, only if a depot has one (a directory depot has none).
- To a notification service (Pushover, Telegram) only if a user names one:
  this setup names none.
- To the edge (my.yardr.dev) only if connected: this setup does not connect.
- Nothing to yardr's makers: yardr has no update check and no telemetry
  unless `yardr otel` is pointed somewhere.

**What the box stores**, all under the yard's OS user (§2.8): `yard.db`
(every bead, note, mail, ask and, for 90 days, event); `blobs/` (files sent
by mail); the logs (never pruned); the documents in the depot folders and
worktrees; each person's Claude login and Claude Code's transcript of each
session in that person's folder; source credentials; backups of `yard.db`.
Nothing is encrypted by yardr; encryption at rest is the disk's.

**Who can read it.** Every agent, the daemon and the server run as one OS
user, so the yard's admin, and any agent on the box, can read every
person's login folder and transcripts. That is the known limit of yardr-2k0x
§3.5, and a fact to state, not hide.

**Retention and deletion.** Events 90 days, webhook bodies 30 days, beads and
notes forever, logs forever. No command erases a bead, a person's mail, or a
person's transcripts; removing a user removes their devices and grants, not
what they said. A team will ask for erasure of one person's data. The
smallest version: `yardr bead erase <id>` (its notes, events, blobs and
worktree, recorded as one event that says it was erased and by whom), and
`yardr user rm --erase` (their mail, their originated beads by the same
erase, their login folder); retention as settings rather than environment
variables; and log rotation by size.

**Size:** 3 beads (bead erase; user erase on top of it; retention settings
and log rotation). The facts list itself goes into the page of §3.7.

### 3.7. Install and upgrade by the team's IT operator

What carries: `contrib/server-yard`, `yardr doctor` and `yardr setup`
(§2.9) cover programs, services, linger, the yard's state and the setup
steps; the hosted image (§2.10) already pins the three versions that work
together.

What is missing, smallest first:

- **One page**: docs/team-server.md, "A team server", which an IT person
  follows from a bare Linux box to the first person's bead: the programs and
  pinned versions, server-yard, the network steps of §3.4, adding a person
  (§3.8), the synced folder of §3.5, the facts of §3.6, backups of the whole
  home and not only `yard.db`, and upgrading by archive. 1 bead.
- **server-yard run once on a fresh Linux box**, fixing what breaks, with
  the result in its README. It runs under a service account with linger,
  which IT may prefer to a person's account. 1 bead.
- **The archives where a team can fetch them**: today they need a GitHub
  account on the private repository. Until that changes, a checked archive
  and its `checksums.txt` are handed over, and the page says how to verify
  it. No bead, a decision (§5, question 6).
- **Upgrade from an archive**: `yardr upgrade` builds from source with Go,
  which a team's server should not need. Replacing the binary works today
  (the server and daemon re-exec themselves); the page says so, with the
  backup step first. A `yardr upgrade --archive <file>` that verifies the
  checksum, backs up and rolls back as `upgrade` does: 1 bead, after the
  first team has started.

**Size:** 3 beads before the first team starts (the page, server-yard on a
fresh box, the onboarding of §3.8), 1 after.

### 3.8. Found while reading: adding a person is eight steps

Each person needs a user, a device or a JWT mapping, a folder and env file
for their login, a group with `login_for`, a route with `--originator`, the
yardmaster's `for=` and `env_for_<user>`, and a led login whose code they
paste back (§2.2). docs/groups.md shows each step; setting up each person
takes eight steps, and a slip bills one person's work to another's login.
One command closes it:

```text
$ yardr user add ada --login claude
user ada added
  login folder ~/.yardr/logins/ada (0700), env ~/.yardr/groups/ada.env (0600)
  group ada-builders (kind claude, login_for ada), route --originator ada for the stages builders take
  yardmaster serves ada (env_for_ada)
  ada's sign-in is asked of ada: the yardmaster mails the link, ada pastes the code back
next: yardr user device pair ada laptop --url https://yard.internal.example
```

It makes nothing new: each line is an existing command, in the order
docs/groups.md gives. Undoing it is new behaviour. `yardr user rm` today
(`deleteUser` in `internal/store/users.go`) removes the user's record, their
devices, their grants `--as` them, their credential, notifications and
pushes, and nothing else: the group with `login_for ada`, its route, the
yardmaster's `for=` entry and its `env_for_ada` setting all stay, and a
route would then send beads `--originator ada` to a group whose login
belongs to no one. So `user rm` also removes those records, in the same
write; the login folder and env file stay on disk until the erase of §3.6
(`user rm --erase`), since they hold the person's transcripts.
**Size:** 1 bead, the command and the undoing together.

A person at footplate cannot create a bead (no create call on the listener,
§2.1); they ask the yardmaster, whose session on their login files it with
`--for`. That is the natural shape of the work of §3.5 and needs nothing.

### 3.9. What carries over from the hosted service, and what does not

Carries over: the image's pinning of yardr, herdr and claude together; the
entrypoint's way of running a yard without systemd, if IT wants a container;
the web view behind a gateway that mints a JWT; the led login relayed by the
yardmaster; footplate's pairing by address.

Does not: the edge and Kratos (a team server has its own login and must not
send traffic to my.yardr.dev); the provisioner (one yard, not one per
person); the hosted service's machine boundary (it puts each person's yard
on a separate machine; team members share one yard, one OS user and one
store, which is why §3.2 and §3.6 exist at all).

### 3.10. The order to build in

Before the first team starts, on the box (it can start when these land):

1. Proxy and CA variables passed through (§3.4).
2. `yardr user add --login` (§3.8).
3. server-yard on a fresh Linux box (§3.7).
4. Several operators and the answerer and inbox checks (§3.2, smallest
   version).
5. The page "A team server" (§3.7), written last so it names what 1-4
   built.

Then, as a running team needs them:

6. Bead and user erase, retention settings, log rotation (§3.6), before any
   real data enters the yard.
7. The policy seam (§3.2), then depot members and visibility.
8. The source binding (§3.2), if the first pack reads as each person; then
   the first source pack, for wherever the team's documents live (§3.5).
9. A paired-only Windows footplate (§3.3), if the web view is not enough.
10. The module apart (§3.2), once yardr-avjm's licence question is decided.
11. Upgrade from an archive (§3.7).

## 4. Left out

- **Building any of it, pricing, the licence decision, a demo yard**: the
  bead leaves them out.
- **An OS user per person.** It is the real wall between team members'
  logins (§3.6), and the largest change in this list: the runner, herdr,
  file modes and the store's notion of a caller all change. A team server
  states the limit instead; a team that cannot accept it is told before it
  starts.
- **A native Windows yard.** yardr-ens6 already plans it after WSL2 and a
  native footplate; nothing in a team server needs the yard on Windows.
- **Single sign-on inside yardr.** The web view verifies a gateway's JWT,
  and a device a token; yardr does not speak OIDC itself: design yardr-scpj
  chose a gateway in front for the web view, and the same holds here.
- **Groups of people as role holders** (an Active Directory group mapping to
  a role). People are named individually.
- **Audit export.** The event log already records who did what; a report of
  it for compliance waits for a team that asks for one.
- **The terms question.** mem-wcn7 keeps it as a known limit, not a blocker;
  §5, question 4 asks only which plan the logins run under.

## 5. Open questions

1. **Windows.** (a) People on Windows use the web view behind the team's
   login, footplate on Mac and Linux: no code. (b) Build the paired-only
   Windows footplate first: 3 beads and a build machine or disk. (c) Mac and
   Linux only: excludes everyone on Windows. I would take (a).
2. **How much of roles before a first team.** (a) None: one operator, all see
   all; no code, but every approval waits on one person. (b) Several
   operators plus the answerer and inbox checks: 2 beads. (c) The full model
   with depot members and readers: 6 beads. What (c) guarantees is what a
   person sees through footplate and the web view, and nothing about what
   the shared crew reads or repeats to them (§3.2, "What the roles do not
   guarantee"); filtering a conversation pane's reads by its person is 1
   bead more and still not a wall against an agent with a shell. Where
   people of the team must not see each other's work, the option is (d):
   one yard per set of people that must stay apart, on one box or several,
   each with its own OS user, and no roles code; the cost is an operator's
   setup per yard. I would take (b); then (c) if the team wants tidier views
   and people may see each other's work, or (d) if they may not.
3. **Where roles live.** (a) In the core, no seam. (b) The policy seam in the
   core now, a module only once yardr-avjm decides: 2 more beads, behaviour
   unchanged. (c) A module apart from the start: (b) plus the module. And,
   whenever the module comes, its boundary (§3.2): (i) the same repository
   and module, its code behind a `roles` build tag and its own licence
   file, two binaries from one tree: 1 bead, no public Go API; (ii) another
   repository, through public `policy` and entry-point packages with a
   checked version: 2 beads, and a Go API the core keeps stable at every
   change to the list of actions. I would take (b) now, and (i) when the
   module comes, unless yardr-avjm's licence needs the code in another
   repository.
4. **Which plan's terms the logins run under.** (a) Each person's own
   consumer subscription, on mem-wcn7's reading. (b) The team's Claude
   Team or Enterprise seats, each person signing in to their seat: the same
   mechanism, under the commercial agreement of the team's organisation.
   (c) An API key per person (`harness/claude/<user>.env`, which no ask
   writes yet): billing by usage. I would take (b) for a team whose people
   already hold Claude seats: it is the contract their organisation has
   already accepted. The team can say which.
5. **The first source, and whose identity it reads as.** (a) A synced
   folder as a directory depot, no pack, holding only documents every
   participant may read (§3.5). (b) Microsoft Graph read: 4 beads.
   (c) Confluence read: 2 beads. For (b) or (c), it reads either as one
   service account IT scopes for the whole team (no more beads; everyone's
   agents read the same documents), or as each person with their own
   credential (1 bead more, the source binding of §3.2, before the pack). I
   would take (a) to start, then whichever service the team's documents
   live in, read as each person if the team's people may not all read the
   same documents, else as a service account.
6. **How a team gets the binary.** (a) A checked archive is supplied for
   each release. (b) Read access to the private repository for the
   operator's GitHub account. (c) A public download once yardr goes public
   (yardr-avjm). I would take (a) until (c).
7. **Erasure before a first team or after.** (a) Before any real data
   enters: 3 beads on the way to a first team. (b) After, with the first
   team's data treated as test data. I would take (b) if the team works on
   public or test documents, else (a).
8. **Who operates the yard.** (a) An IT operator on the team's side, with
   no remote maintainer access. (b) A maintainer operates it remotely. (a)
   keeps every document inside the team's network; (b) gives the maintainer
   access within the team's network and its security controls. I would take
   (a).
9. **What an operator may do away from the box.** Today the doors carry
   approvals, secrets and reading the grants, and nothing that administers
   (§3.2). (a) Nothing more: users, devices, depots, sources and grants are
   added at the box's shell by the IT operator; no beads. (b) Adding a
   person and pairing their laptop from the web view, for an operator who
   is not IT: new calls on the listener with an operator check at the door,
   2 beads (the calls and their checks; the web form). (c) All
   administration through a door: every admin write a listener call, in
   the order a remote operator meets them; 5 beads or more, and the
   listener stops being a person's narrow door. I would take (a) for a
   first team, where the operator is IT at the box (question 8).

## 6. The beads it would become

In the order they would land. B1 to B6 come before the first team starts; the
rest follow it.

- **B1. Pass the proxy and CA variables to the processes the yard starts.**
  Goal: a box behind an egress proxy works with the standard variables.
  Where: `server.ServerEnv` (`internal/server/server.go`), `serviceEnv`
  (`internal/cli/service.go`), the herdr unit `yardr up --login` writes.
  Done when: `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`, `SSL_CERT_FILE`,
  `SSL_CERT_DIR` and `NODE_EXTRA_CA_CERTS` reach the server, the daemon, the
  web view and herdr; a test of `ServerEnv` and `serviceEnv` names them;
  docs/configuration.md says so. Depends on nothing.
- **B2. `yardr user add <user> --login claude` makes the person whole.**
  Goal: one command per person (§3.8). Where: `internal/cli/users.go`,
  docs/groups.md "Whose login a group carries", `internal/cli/harnesslogin.go`.
  Done when: the command makes the folder, the env file, the group with
  `login_for`, the route `--originator`, the yardmaster's `for=` and
  `env_for_`, and opens the relayed login; `yardr user rm` (`deleteUser`)
  now also removes the group whose `login_for` is the user, its routes, the
  yardmaster's `for=` entry and `env_for_<user>` in the same write, which it
  does not today, and leaves the login folder for B8; a test against a
  throwaway store checks the records both ways. Depends on nothing.
- **B3. Several operators: a role on the user.** Goal: more than one person
  approves through a door, and administers at the shell. Where: `User` and `operatorOnly`, `notOperator`
  in `internal/store/users.go`; `isOperator`, `operatorCall` in
  `internal/rpc/user.go`; a migration making the `operator` setting's user
  the first operator. Done when: `yardr user set ada role=operator` (by an
  operator) lets ada's device approve a run ask, give a secret, approve a
  setup ask and read the grants (`operatorCall` reads the role instead of
  comparing with the `operator` setting); `user add`, `depot add` and
  `source grant` take ada as the actor at the shell; the `operator` setting
  still names the fallback addressee; docs/users.md and docs/api.md say it.
  No admin call is added to the listener. Depends on nothing.
- **B4. An ask is answered by whom it was put to, and an inbox read by its
  user.** Where: `answerable` and `addAnswer` (`internal/store/asks.go`),
  `UserInbox`, `AckUserInbox` (`internal/store/users.go`). Done when: an
  answer from anyone but `ask.To` or an operator is refused in the store;
  another user's inbox is refused to a non-operator; tests in the existing
  ask and inbox tables. Depends on B3.
- **B5. server-yard on a fresh Linux box.** Goal: it works where it has not
  run. Where: `contrib/server-yard`, docs/install.md "On a server". Done
  when: a fresh Debian VM goes from nothing to a doctor without faults under
  a service account with linger; what broke is fixed; the README records the
  run. Depends on nothing.
- **B6. The page "A team server".** Goal: an IT person installs,
  connects and adds people without a yardr maintainer. Where: a new
  docs/team-server.md, linked from docs/README.md and docs/install.md;
  §3.4, §3.5, §3.6 and §3.7 of this design. Done when: the page covers the
  box, the network steps, `user add --login`, the synced folder, the data
  facts, whole-home backups and upgrading by archive, several operators,
  and that an ask is answered only by whom it was put to; `yardr docs
  team-server` prints it. Depends on B1, B2, B3, B4, B5.
- **B7. `yardr bead erase`.** Where: `internal/store/beads.go`, the event
  log, blobs, worktrees. Done when: a bead's notes, events, files and
  worktree are gone, one event records the erase and its actor, operators
  only; a test with a throwaway store. Depends on B3.
- **B8. `yardr user rm --erase`, retention as settings, log rotation.**
  Where: `deleteUser`, `internal/store/prune.go`, the log writers. Done when:
  a person's mail, originated beads (through B7) and login folder go; the
  three retention variables are settings too; logs rotate by size. Depends
  on B7.
- **B9. The policy seam.** Goal: every gate asks one function. Where: the
  call sites listed in §3.2, the CLI-only gates. Done when: behaviour is
  unchanged (the existing tests pass untouched) and a policy registered
  from `main` can replace the rules, inside the module (the boundary is
  B14's); the CLI-only gates are in the store. Depends on B3, B4.
- **B10. Depot members and what a person sees.** Where: `Depot`
  (`internal/store/graph.go`), the read calls a device makes
  (`internal/rpc/user.go`), the web lists. Done when: a member sees only
  their depots' beads and their own through a door; the operator sees all;
  `reader` exists; the first restriction set writes `policy_required=roles`
  (§3.2); docs/users.md says the rule limits the doors, not what the shared
  crew reads. Depends on B9.
- **B11. A conversation's source calls use only its user's credential.**
  Goal: a grant `--as X` serves only X's work (§3.2, "Sources"). Where:
  `sourceCallerIn`, `may`, `callables` (`internal/store/sourcecalls.go`),
  `paneCrewSession` (`internal/store/caller.go`). Done when: from a crew
  member's conversation pane for X, a call `--as X` is allowed and `--as Y`
  refused; the line session of a crew member that keeps conversations, and
  a crew call told only by its actor, use no grant `--as` a user; a bead's
  session uses `--as X` only when X is the bead's originator; `callables`
  lists only what the caller may use; rows in the existing source call
  tests; docs/integrations.md says it. Depends on nothing.
- **B12. The first source pack**, for wherever the team's documents live
  (Confluence: 2 beads; Microsoft Graph: 4, the first being file bytes into
  a worktree in the source format). Depends on B11 if it reads as each
  person, on nothing if as a service account.
- **B13. A paired-only Windows footplate** (footplate depot, 3 beads: the
  cfg split and the key store; pairing as the only first screen; the build
  and release). Depends on a build machine.
- **B14. The roles module apart**, by the boundary §5 question 3 takes: a
  `roles` build tag in this module with its own directory, licence file and
  second release binary (1 bead), or public `policy` and entry-point
  packages with a version check plus the other repository and its release
  (2 beads). Done when: both binaries build and pass the gate; the core
  binary behaves as before B9 on a yard without restrictions and serves no
  door on one whose `policy_required` is `roles` (§3.2, "fails closed");
  `yardr upgrade` on the roles binary builds the roles binary. Depends on
  B9, B10 and yardr-avjm.
- **B15. `yardr upgrade --archive <file>`.** Where: `internal/cli/upgrade*.go`.
  Done when: a checked archive installs with the backup, gate and rollback
  `upgrade` has. Depends on nothing.
