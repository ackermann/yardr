# A team server

One Linux box on the company's network, running one yard for a team.
Each person works on their own seat of the company's Claude plan,
reaches the yard through footplate or the web view, and talks with the
crew, who file beads for them; the team's documents reach the agents as
a folder. This page takes an IT operator from a bare box to the first
person's bead, with nothing from a yardr maintainer: no maintainer has
access to the box, and the operator runs it wholly.

Examples use `ops` for an operator, `ada` and `bea` for people,
`knowledge` for the directory depot, and `yard.internal.example` for the
yard's name. Replace them and the `example.com` addresses with values for
your yard.

The company gives three things first: an internal name for the yard
(`yard.internal.example` below) with a certificate for it, and, if the
box reaches the internet only through one, an egress proxy and its CA.
The rest is on this page: the box, the install, the network, the
documents, the people, what is stored and where, backups, and upgrading
by hand.

## The box

A Linux server reached over ssh. The yard runs as an account of its
own: a regular user with a home and a shell (`sudo useradd --create-home
--shell /bin/bash yardr`), reached by ssh or `sudo -iu yardr`.
`loginctl enable-linger` lets its services run with nobody logged in; on
a server without polkit a person with root runs it once for the account.
Four programs have to be on the PATH of the account's shells: yardr
(next section), herdr, a harness, and git.

- **herdr**, which starts the agents' panes: its installer is at
  <https://herdr.dev/install.sh>, and
  <https://herdr.dev/docs/install/> says the ways.
- **Claude Code**, the only harness a team server runs:
  `npm install -g @anthropic-ai/claude-code`
  (<https://claude.com/claude-code>), or Anthropic's installer
  (<https://claude.ai/install.sh>).
- **git** beside them: [install it on Linux](https://git-scm.com/download/linux).
  On Debian, run `sudo apt install git`.

Note which versions run together; `yardr doctor` prints them. Upgrade
the three together, and check the new three answer before the old ones
are relied on no longer.

## Installing from the archive

Each release is handed over as three files: the archive whose name
matches the box (`yardr_<version>_linux_amd64.tar.gz` or
`yardr_<version>_linux_arm64.tar.gz`), `checksums.txt`, and the
signature `checksums.txt.minisig`. The archive holds `yardr` beside
`server-yard`, the script that sets the yard up. Check the handover
before anything of it runs: the signature first, so the checksums are
believed, then the archive against them
([install.md](install.md#from-a-release-archive),
[signed checksums](install.md#signed-checksums)):

```sh
sudo apt install minisign # once, on Debian
minisign -V -P RWSzUPGBbv7MDqH+nnbNmYdOm20UTkuk7eC5e6XuVtYRwZtaEZRlV/WB -m checksums.txt
sha256sum --ignore-missing -c checksums.txt
```

On another Linux distribution, follow [minisign's install
instructions](https://jedisct1.github.io/minisign/).

Both must say the files are good. Then unpack the two programs and put
the yard's where every shell finds it:

```sh
tar -xzf yardr_<version>_linux_<arch>.tar.gz yardr server-yard
mkdir -p ~/.local/bin && mv yardr ~/.local/bin/     # or /usr/local/bin
yardr --version            # the release's tag and revision
```

A login shell started before `~/.local/bin` existed has no PATH for it:
log in again, or run `. ~/.profile`, first. One script then does the
setup, from a login shell of the yard's account, and a second run of it
changes nothing
([On a server](install.md#on-a-server-no-desktop)):

```sh
./server-yard --harness claude --operator ops --wait 20s
```

It writes herdr's server as a systemd user unit, turns linger on,
creates the yard, installs the daemon and the web view as login
services, and ends with `yardr doctor`. It asks nothing, and the login
is not its step: the harness login is the operator's, in the
yardmaster's pane once the yard exists (`herdr agent attach
yardr-yardmaster`, then run `claude` there and log in, as its own
documentation says). `yardr doctor --try` then makes one real call to
it, which proves the box reaches Anthropic at all
([doctor.md](doctor.md#--try-one-real-call-to-each-harness)).

The name `--operator` was given is who administering commands act as,
and the account's login name is not that name, so set it for this shell:

```sh
export YARDR_ACTOR=ops
```

Without it the commands below that change a setting or add a user are
refused, naming the operator. Instead of exporting it, pass the same
name to an individual command as `--actor ops`.

## The network

yardr ends no TLS and builds no proxy: a reverse proxy of the company's
stands in front, on the same box, with IT's certificate. The steps, in
order:

1. **The listener on loopback.** The yard's listener takes footplate's
   calls; without an address it listens nowhere:

   ```sh
   yardr settings set hook_addr 127.0.0.1:8790
   yardr server stop     # the next command starts it again, listening
   ```

   The server reads the address when it starts, so a change takes its
   next start.

2. **A reverse proxy on the same box**, ending TLS with IT's certificate
   and passing `https://yard.internal.example/user/` to `127.0.0.1:8790`,
   and nothing else of the listener (`/hooks/` and `/peers/` stay closed
   unless a webhook source needs one). It must run on the box itself: a
   secret, and an approval that runs a command as the operator, is taken
   only from a request that reached the listener from the yard's own
   machine. Throttling, if any, is the proxy's: yardr never answers 429.

   The proxy must not send `X-Forwarded-Proto: http`. yardr reads that
   header as the scheme the request came in on, and refuses a secret or
   an approval that says it arrived in plain HTTP: a token read off a
   network in the clear would otherwise have the operator's commands run
   from wherever it was read. A proxy that ends TLS says `https`, as the
   request came to it.

3. **The web view behind the company's login.** An auth gateway in front
   of the view logs each visitor in and forwards a signed ID token; yardr
   validates it and maps its email claim to a user. Against Entra that is
   [oauth2-proxy](https://oauth2-proxy.github.io/oauth2-proxy/) with its
   [Microsoft Entra ID
   provider](https://oauth2-proxy.github.io/oauth2-proxy/configuration/providers/ms_entra_id),
   on the box, passing the ID token as `Bearer` in `Authorization`
   (its `--pass-authorization-header`). The token must carry an `email`
   claim: yardr maps that claim, and no other, to a user of the yard, so
   the sign-in asks for the `email` scope. Entra can omit the claim when
   an account has no associated email address, and the address can change;
   check each token before mapping its email, and use only addresses the
   identity provider verifies. A signed token proves what the provider
   asserted, not that the person controls the address ([Microsoft's ID
   token claims](https://learn.microsoft.com/en-us/entra/identity-platform/id-token-claims-reference),
   [yardr's trust rule](web.md#behind-an-auth-gateway)). Then the five
   settings, one
   `email=user` pair per person in the last, and the view installed under
   the yard's name ([Behind an auth
   gateway](web.md#behind-an-auth-gateway)):

   ```sh
   yardr settings set web_jwt_issuer https://login.microsoftonline.com/<tenant-id>/v2.0
   yardr settings set web_jwt_audience <the OAuth client id>
   yardr settings set web_jwt_jwks https://login.microsoftonline.com/<tenant-id>/discovery/v2.0/keys
   yardr settings set web_jwt_header Authorization
   yardr settings set web_jwt_users ada@example.com=ada,bea@example.com=bea
   yardr web install --host yard.internal.example
   ```

   The issuer and the keys are the tenant's, from its OpenID
   configuration; the audience is the client id of the app registration
   oauth2-proxy signs in with. Setting the issuer turns JWT mode on: the
   view verifies the token on every request but `/health`, each request
   acts as its own user, and a token whose email maps to no user is 403.
   The view stays on loopback; only the gateway faces the network.

4. **The pages origin**, which serves the files a run attaches or makes
   (`//yard.internal.example:8789/blobs/<hash>`) from an origin apart
   from the view's, so a file cannot act as the person. The proxy
   forwards that port too, to where the origin listens, with the Host
   header unchanged. Where IT would rather open no second port, it gives
   a second name instead (`pages.yard.internal.example`, with its
   certificate) and the operator adds `--pages-host
   pages.yard.internal.example` to `web install`. Without one of the
   two, a web user cannot open an attached or generated file
   ([Behind a proxy](web.md#behind-a-proxy)).

5. **Pairing**, per person per laptop
   ([Adding people](#adding-people)):

   ```sh
   yardr user device pair ada laptop --url https://yard.internal.example
   ```

   The code goes to the person, who types it into footplate. It is good
   for ten minutes and for one device; show it to the device and to
   nobody else.

6. **Egress**, where the box reaches the internet only through a proxy.
   yardr sets none of the variables and hands on the ones its starter
   has, to the server, the daemon, the web view and herdr's server
   ([Behind an egress
   proxy](configuration.md#behind-an-egress-proxy)):

   | variable | what it does |
   |---|---|
   | `HTTPS_PROXY`, `HTTP_PROXY`, and their lower-case forms | name the proxy for https and for http requests |
   | `NO_PROXY`, and `no_proxy` | names the hosts reached without it: the box's own name, a JWKS URL inside the company |
   | `SSL_CERT_FILE`, `SSL_CERT_DIR` | name the roots Go trusts, for a company CA outside the system's store |
   | `NODE_EXTRA_CA_CERTS` | names extra roots for claude, which runs on Node and does not read the system's store |

   `server-yard`, `yardr daemon install`, `yardr web install` and `yardr
   up --login` write the ones their shell has into the services they
   write, which the agents' panes inherit from herdr's server; an
   `env_file` sets any of them for one group's panes alone. The proxy's
   CA goes in the system store for yardr's own calls, which Go on Linux
   reads. A proxy's address can hold a password, so the service files
   are mode 0600; prefer a proxy that lets the box through by its
   address. After a variable changes, run the installs again: a service
   keeps the environment it was written with.

7. **Nothing to the edge.** A team server does not run `yardr edge
   connect`: the edge is yardr's hosted service, outside the company's
   network, and sees what crosses it ([edge.md](edge.md)).

## The documents

The first use a team server is for is knowledge work: agents read a
body of documents and write answers a person checks. The documents
reach the agents as a folder, a directory depot over it: no pack and no
source, synced by a sync IT already allows, run by the operator outside
yardr, on a timer. `rclone sync` of one SharePoint library or OneDrive
folder, or a mounted file share, does it; rclone's
[OneDrive](https://rclone.org/onedrive/) documentation covers
SharePoint too, and its [install page](https://rclone.org/install/) the
install.

`rclone sync` makes its destination match the library, so it deletes
whatever else it finds there, an agent's answer included. The depot's
folder therefore has two parts that never overlap:

```text
/srv/yard/knowledge/            the directory depot below
  library/                      rclone sync's destination: the documents, replaced at each run
  answers/                      the agents' outputs: no sync writes or deletes here
```

The sync names `library/` and nothing above it (`rclone sync
sharepoint:Knowledge /srv/yard/knowledge/library`, with the remote as
`rclone config` made it), and each bead names its outputs under
`answers/`. An agent that writes into `library/` anyway loses that file
at the next run, which is the sync working, not a loss of the yard's
work. Where IT wants that enforced rather than asked, the sync runs as
another OS user that owns `library/`, and the yard's user gets read
access only; with a mounted file share, the mount is read-only.

That folder is one corpus for everyone. The sync reads with one
identity, so it holds what that identity may read, and every agent of
the yard, serving any person, reads all of it. **The folder holds only
documents every member of the team may read.** Where access differs
between people, that folder does not fit: either one folder per group
of people allowed the same documents, or no folder route at all.

The depot, then its way of working, from the `dir` pack yardr carries,
with both agent groups on claude
([dir-depots.md](dir-depots.md)):

```sh
yardr depot add knowledge /srv/yard/knowledge --kind dir
yardr depot init knowledge --yes --bind builders=claude --bind reviewers=claude \
  --members backlog= --members decisions=ops
```

It stands before the first person is added: `user add --login` copies
the yard's builders, and refuses where there are none. A dir depot gets
no test command: nothing in a folder of documents says how it is
tested.

The pack's reviewers are agents. For a person to check each answer,
through `yardr review`, send `review` to a manual group afterwards:

```sh
yardr group add knowledge/review --runner manual
yardr route add --stage review --depot knowledge --group knowledge/review --originator any
```

## Adding people

One command per person, after the depot above stands. It makes the
user, their login folder and env file, a group of their own on claude,
routes sending their beads to it, their conversation with the
yardmaster on their login, and opens the sign-in, which they finish
themselves ([users.md](users.md#the-record),
[groups.md](groups.md#a-person-with-a-login-of-their-own)):

```sh
yardr user add ada --login claude
```

The page to sign in on is mailed to them, and the code it shows comes
back as an ask of their own, which goes to the waiting login and is
kept nowhere. They read the mail and give the code in a device, so a
new user is paired one while the sign-in waits, which takes up to a
quarter of an hour. The sign-in ends with one short call on their seat,
which proves the login answers. One that did not finish is opened again
with `yardr user login ada`. Each person signs in to their own seat of
the company's Claude Team or Enterprise plan.

### Who reaches it how

People on Mac and Linux install footplate, the desktop app, from its
releases on yardr.dev, and pair it by the code above
([Enrolled by a code](users.md#enrolled-by-a-code)). People on Windows
use the web view behind the company's login, from the gateway step
above. From either, a person talks with the crew and answers what is
put to them; beads are filed for them by the yardmaster, since no bead
create is on the listener.

### Operators

There may be several operators, and every check of who may do what
reads the role, so an approval waits on none of them in particular:

```sh
yardr user set bea role=operator
yardr user list     # each user's role
```

An operator approves through a door: run asks, setup asks, secrets, and
reading the grants. An operator administers the yard at the shell:
users, devices, depots, sources and their grants, settings. Only an
operator sets a role, and the yard refuses to make a member of its last
operator. The `operator` setting still names one operator: the one an
ask with no addressee goes to, and whose login and inbox the yard uses
where it needs one person's ([users.md](users.md#the-record)).

Administration happens at the box's shell; nothing that administers the
yard is on the listener. From a device an operator approves run asks,
gives secrets and approves setup cards, and nothing more of the
yard's keeping ([users.md](users.md#devices)).

### An ask is answered only by whom it was put to

A plain ask has one addressee, and the store takes its answer only from
them or an operator; anyone else's is refused, naming the addressee.
Run asks keep their own rule — any operator approves one, whoever of
them it was put to — and a stage ask is its members' to answer. An
operator reads and acknowledges any user's inbox; a member reads only
their own ([users.md](users.md#the-record)).

### Removing a person

```sh
yardr user rm ada
```

It removes the user, their devices, the group that carried their login
with its routes, every route for their beads, and them from the
yardmaster's conversation, in the same write. It leaves the login
folder and the env file on disk, with the transcripts in them, and says
where they are: no command erases them yet. A user added again under
the name is refused until they are moved away.

## What is stored, and where

Until per-person erasure exists, use only public or test documents in
the yard. Which rules bind a team's use of it is for the team to apply;
these are the facts, as the code stands.

**What leaves the box.**

- To Anthropic, through each person's Claude login: every prompt, every
  file an agent reads, every answer; under the terms of the plan that
  login belongs to. Claude Code also sends its own telemetry and error
  reports, which its own settings turn off.
- To each declared source: the calls the agents make, logged as
  `source_call` events. This setup declares none.
- To git remotes, only if a depot has one. A directory depot has none.
- To a notification service, only if a user names one. This setup names
  none.
- To the edge, only if connected. This setup does not connect.
- Nothing to yardr's makers: yardr has no update check and no telemetry
  unless `yardr otel` is pointed somewhere.

**What the box stores.** Yardr's home ([yardr's own
state](configuration.md#yardrs-own-state)) holds `yard.db` (every bead,
note, mail, ask and, for 90 days, event); `backups/` (dated copies of
the store); `blobs/` (files sent by mail); the logs (`server.log`,
`daemon.log`, `web.log`, `logs/sessions/`, never pruned); the worktrees;
each person's login folder and env file, with Claude Code's transcript
of each of their sessions; source credentials; and the yard's key.
Depots live at the paths the operator registers: this guide's directory
depot is `/srv/yard/knowledge`, which holds the synced documents and
answers. Nothing is encrypted by yardr; encryption at rest is the disk's.

**Who can read it.** Every agent, the daemon and the server run as one
OS user, so the yard's admin, and any agent on the box, can read every
person's login folder and transcripts. That is a fact to state, not
hide. A device of a user who is no operator reads every bead with its
notes, and the mail of its user alone.

**Retention and deletion.** Events 90 days, webhook bodies 30 days, as
environment variables, not settings; beads, notes and logs forever
([Retention](configuration.md#retention-keeping-yarddb-bounded)).
`yardr user rm` keeps the person's mail in the log. No command erases a
bead, a person's mail, or a person's transcripts, short of `yardr
uninstall --purge`, which deletes the whole yard.

## Backups

The daemon keeps dated copies of the store once it is told how often,
through the running server, so nothing stops
([Copies of the store](configuration.md#copies-of-the-store)):

The default is `<home>/backups`. To keep the copies on a separate
volume, mount it at `/mnt/backup` before each copy is due; yardr creates
the `yardr` directory there when the first copy is due.

```sh
yardr settings set backup_every 1d
yardr settings set backup_keep 14
yardr settings set backup_dir /mnt/backup/yardr   # on a mounted volume at /mnt/backup
```

The copies hold the store alone. They do not include the rest of yardr's
home or depots at registered paths. Back up the whole yardr home
(usually `~/.yardr`) and every depot path outside it; for this guide, include
`/srv/yard/knowledge` so the synced documents and answers are restored
too. Restore a copy by stopping the yard and putting the file in the
store's place:

```sh
yardr down
cp ~/.yardr/yard.db ~/.yardr/yard.db.before-restore
cp ~/.yardr/backups/yard-20261013T142233Z.db ~/.yardr/yard.db
yardr up
```

Replace the dated filename with the copy being restored.

Before an upgrade, a copy by hand first (the directory has to exist):

```sh
mkdir -p ~/.yardr/backups
yardr store backup ~/.yardr/backups/yard-before-upgrade.db
```

## Upgrading from the next archive

By hand, from the next archive: the box needs no Go. Check the handover
as the first was checked — the signature, then the checksums — back the
store up as above, and move the new binary over the old one, keeping
the old one aside until the check below passes. The server and the
daemon notice the new file and replace themselves; a newer build
changes nothing in a yard that is set up
([Upgrading](configuration.md#upgrading)).

Then prove every part runs it. `yardr upgrade --check` asserts the yard
runs this build — the build of the `yardr` that was asked — read-only,
one line per check ([The gate](upgrade.md#the-gate)):

```sh
$ yardr upgrade --check
server    ok: answers on <pid>, this build
daemon    ok: holds the lock on <pid>, this build, pass <seconds> s ago
views     ok: <count> answers on <pid>
```

`<pid>`, `<seconds>` and `<count>` stand for the values the command
prints.

The web view does not replace itself: it keeps running the build it was
started from until it is restarted. Run `yardr web install` again with
the same flags it was installed with — an install writes only what it
is given, so the names go again, or they are dropped:

```sh
yardr web install --host yard.internal.example
```

If the web view uses a separate pages name, repeat that flag too:

```sh
yardr web install --host yard.internal.example --pages-host pages.yard.internal.example
```

## Operating

Administration happens at the box's shell, by the IT operator. From a
device an operator approves run asks, gives secrets and approves setup
cards. `yardr doctor` is the daily check: it names what is missing or
faulty, with the fix beside each. After a reboot the services come back
with nobody logged in, the daemon's first pass finding the sessions
without their panes and starting them again
([Login services](configuration.md#login-services)).

No yardr maintainer has access to the box, and none is needed past this
page: the yard's own documentation is in the binary (`yardr docs`
lists it, `yardr docs <name>` prints one page), and every command
answers `--help`.

## What this page leaves out

Erasure, which is deferred: until it exists, only public or test
documents enter the yard. Source packs: the documents come as a synced
folder instead, and a pack for wherever they live comes later. A hosted
install: the yard stands on the company's box and connects to no edge.
