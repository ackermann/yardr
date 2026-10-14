# Hosted v0: a person signs up at the edge and gets a yard container on the Mac

Design yardr-bwm2, proposed 2026-10-14. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. A depot is a git repository or a plain directory that beads are worked in; a bead is one durable piece of work; the crew are the yard's always-on agents, and the yardmaster is the one that coordinates; a harness is the agent program herdr starts (claude, codex); the operator is the person who runs the yard; a user is a person of the yard, a name and an inbox; a device is one program of a user that calls the yard from outside it; the edge is whatever stands in front of the yard. footplate is the native window over a yard, in its own repository and depot. See docs/glossary.md for all of yardr's words. This design is the first step of design yardr-8wxe (docs/design/2026-10-12-yardr-8wxe-yard-as-a-service.md) made concrete, not a second opinion: its shape 1, on this Mac, for a trial. "The service" below means the thing a person signs up to, "the customer" the person who gets a yard, and "the Mac" mckean's machine, which hosts the trial.

## 1. The question

mckean, 14 Oct 2026 (mails 30987, 31028, 31072): "the plan is to make yardr hostable, so users can sign up and they get their yard." His shape: a single Kratos where people sign up; behind it a yard per user, a Docker container for now, VMs (EC2, k8s) later; the user connects to the web view or footplate and onboards themselves; Kratos maps users to yards, Oathkeeper handles access, Hydra for footplate's token. Build on the Mac, in this yard, as the testbed; for the trial "just make it work technically".

## 2. What exists today

### 2.1. The service design this builds on

Design yardr-8wxe proposes shape 1, a yard per customer hosted whole, with the wall between customers being the machine (its 3.2), the service's pieces listed in its 3.3 (sign-up and the edge, provisioning, the GitHub App with broker and router, git, a way to the login pane, being told, upgrades and backups, metering, stopping idle machines), the yardr changes in its 3.4 (a server script, the view acting as one named user, a person handing the yard a secret), the customer's path in its 3.5, and risks in its 3.6. Its open question 2 weighed a virtual machine each against a microVM each against a container each, and the draft would not take the container; mckean's decision for this trial takes it (D1 below). Its open question 5 weighed where the service's code lives and would take a repository of its own; this design does that (section 3.2). Its 3.7 is the trial this design's trial replaces: one outside person, one machine, one week, reached over ssh — here the person never sees a terminal.

### 2.2. A yard from one script, and what it still assumes

`contrib/server-yard/server-yard --harness <kind> --operator <name>` (8wxe B1, landed) sets a yard up on a fresh Linux machine: herdr's server as a systemd user unit, linger so the services run with nobody logged in, `yardr init --harness <kind> --operator <name> --start`, the daemon and the web view as login services, ending with `yardr doctor` (its README). A second run changes nothing. It assumes a systemd user session, which a Docker container does not have; section 3.4 says what runs in the container instead. `yardr up` and `yardr down` (internal/cli/updown.go) bring the yard up — herdr's server, the yard's server, the daemon — and stop it again, safe to repeat, with `--json` for a program.

### 2.3. The web view acts as one named user

`yardr web serve --as <user>` (design 8wxe's B2 and B3, beads yardr-y6ty and yardr-bvt3, both merged) shows the yard in a browser and, for that user, sends mail to a crew member, shows the conversation, answers asks and moves a bead that waits for a person (docs/web.md, "Acting as a user"). Without `--as` every route is GET only. The user resolves in three layers — `--as`, else `YARDR_WEB_AS`, else the yard's `web_as` setting (internal/store/settings.go, `WebAsSetting`), else read-only. The view listens on loopback, or with `--unsafe` on any address, answering only to the names `--host` gives (421 otherwise; internal/cli/web.go). `yardr web install` is loopback only, so a view that faces the private network is started, not installed, exactly as 8wxe 3.3 item 1 has it: `yardr web serve --unsafe --addr <private address>:8791 --host <the customer's name for it>`.

### 2.4. The yard's half of gateway identity is filed, not built

Design yardr-scpj (docs/design/2026-10-14-yardr-scpj-web-jwt-identity.md), approved 14 Oct: with `web_jwt_issuer` set, the view verifies a gateway's JWT per request against a JWKS URL and maps its email claim to a yard user through `web_jwt_users`. Its beads yardr-rrce (verify per request) and yardr-0hrq (the verified user acts) are in `new` today, parked behind PWA plus web push. Until they land, the model is 8wxe's: the edge admits only the right person, and the view behind it trusts `--as`. After they land, a mistake in the edge's rules is refused at the yard instead of being a breach.

### 2.5. A device enrols by code, and footplate pairs and sets up

`yardr user device pair <user> <device> --url <address>` (internal/cli/devicepair.go; docs/users.md, "Enrolled by a code") opens a ten-minute pairing: an eight-character code, good once, which the device calls with at `POST <address>/user/pair` and is answered with its token. The yard then serves the 21 calls of docs/api.md ("Over the network") under `/user/` to that device as that user. Opening a pairing is the operator's; the yardmaster cannot do it from its pane. footplate's first screen (its README, "No yard") offers three ways, the pair-with-an-address road among them, keeping the token in a 0600 file; and its "Set up a yard here" road (design yardr-3az6, built) runs the first run with no terminal, leading `yardr harness login <kind> --json` for a kind with a recipe.

### 2.6. A harness login led without a terminal

`yardr harness login <kind> --json` plays the kind's login recipe under a terminal of yardr's own and prints what it needs as JSON lines — `choose` (the method menu), `open_url` (the browser sign-in), `input` (the code to paste back), `confirm` — taking answers on stdin and ending with `done` (internal/cli/harnesslogin.go). Only claude has a recipe today (`login_recipe` in harnesses/claude.yaml, read from `claude auth login`, Claude Code 2.1.288). The pasted code is never stored. Design yardr-2k0x (originators, per-person logins, landed with its beads filed) adds `--group` to this command in its B3 and leads a group login from the web view in its B9, after its trial — neither is built, and this design does not wait for them.

### 2.7. A secret handed over, and its hosted limit

A secret ask is answered unseen and written to a file by the server (docs/glossary.md, "ask"); from a device it is the `GiveSecret` call, taken only from a device of the yard's operator and only when the request reached the listener from the yard's own machine (docs/api.md, "For the operator alone"). Behind a gateway on another container that second condition does not hold, so a hosted secret handoff over the edge is a named follow-up, not part of the trial (section 4); the trial hands its one token over in conversation (section 3.7).

### 2.8. The repository reaches the yard through the pack

GitHub is `packs/github`, a pack of kind source: a webhook pointing at the yard's hook listener, a secret file, `yardr hook add`, a credential (a token, or a GitHub App installation token minted by `contrib/github-mint/github-mint` as the source's `mint_command`), the identity, the grants, the daemon's reads (8wxe 2.7). The git transport uses whatever git on the machine is configured with; yardr sets up no git credential. The trial uses a token of mckean's own; the App, broker and router of 8wxe 3.3 item 3 come later (section 4).

### 2.9. The Mac as the testbed

OrbStack runs Docker containers on the Mac today (a supabase set answers `docker ps` now; footplate's Linux shot containers are built the same way), and a dokku VM beside them. The yard's public pieces reach the internet through a Cloudflare tunnel (signalbox.yardr.dev) and tailscale serve. Kratos, Oathkeeper and Hydra are as design yardr-xpf8 read them on 12 Oct 2026 (its 2.7): Kratos holds identities and gives a native app a session token for `Authorization: Bearer`; Oathkeeper authorizes each request as a reverse proxy, with `bearer_token` and `oauth2_introspection` authenticators and `noop`, `header` and `id_token` mutators, needing no database; Hydra is an OAuth2 server that needs a login and consent application, with the device grant among its flows. Kratos is API-only: the sign-up and login pages are someone's to write.

## 3. The proposal

The existing mechanisms carry almost all of it: the server-yard steps, the view with `--as`, device pairing by code, the led harness login, footplate's pair-by-address road, and the edge pieces as Ory ships them. What is new is the service repository with its image, provisioner and edge rules, and footplate's hosted mode. Three points are decided by mckean and written in as decided (D1–D3); five the bead leaves to this design (3.2–3.7).

**D1. The wall for the trial is a Docker container per user on the Mac.** VMs or k8s are for scaling, later. A container shares a kernel with other users' code: fine for a trial with people he knows, named as the limit. Losing: a VM each — the simplest wall and the dearest, always on, a platform to run before anyone has signed up.

**D2. Hosted users bring their own harness credential, subscription login or API key; misuse is on them; the service collects no credential.** The harness's own login screen, or an API key the user hands the yard. Losing: the service collecting a subscription token into a paste field — forbidden as read for Claude (8wxe 2.10), and the harness's own screen exists.

**D3. Identity: Kratos holds accounts; Oathkeeper fronts the browser's way to a yard's web view; Hydra issues footplate's token** through an OAuth2 flow with PKCE, the yard verifying the gateway's JWT per request — beads yardr-rrce and yardr-0hrq are the yard's half. Losing: device tokens alone with no identity service — enough for one person, and no sign-up at all.

### 3.1. The pieces in one picture

```text
browser ── sign-up/login pages ── Kratos (accounts, one for all users)
browser ── Oathkeeper ──┬─▶ user A's container: web view --as A, on the docker network only
                        └─▶ user B's container: web view --as B
footplate ── Hydra (PKCE in the system browser) ── token beside the yard entry
footplate ── Oathkeeper (checks the Hydra token, passes all headers on) ──▶ A's container: /user/* with the device token
GitHub ── webhook ─▶ A's container: POST /hooks/gh (per-yard secret; the App comes later)
provisioner ──▶ image, volume, container, server-yard steps inside, Oathkeeper rules regen

one user's container, one OS user:
  herdr server · yardr server · yardr daemon · yardr web serve --as <user> · the harnesses, logged in by the user
  /home/yard (a named volume: store, packs, depots/<repo>) · nothing of another user
```

### 3.2. The user-to-yard map and provisioning: `yardr-hosted`

The service's code lives in a repository of its own, `yardr-hosted`, added as a depot of this yard (8wxe Q5, decided here for the trial). The map of user to yard is a small SQLite store on the Mac beside the provisioner: user name, Kratos identity id, container name, volume name, the web view's address on the docker network, the host name the edge answers for them, and when the yard was made. A flat file would do for two users and fail at twenty; a database the edge also reads would couple the provisioner to every request. SQLite is the middle: one file, backed up with the volumes' list, read only by the provisioner and the edge's rule builder.

What a sign-up triggers, in order:

1. The account exists in Kratos (the sign-up page, open question 1). The provisioner makes the yard: it builds or pulls the image (3.3), makes a named volume for the user's home, starts the container, and runs the server-yard steps inside it with `--operator <user>` (3.4), with the web view bound to the container's private address and `--host <the user's edge name>`.
2. The provisioner opens the device pairing for footplate (`yardr user device pair <user> footplate --url <the user's edge address>`, run inside the container) and writes the code to the hosted store, where the "your yard is ready" page reads it. The code holds ten minutes; if it lapses, the service operator re-opens it during the trial (self-service re-pairing is a named follow-up, section 4).
3. The provisioner regenerates Oathkeeper's access rules file from the map — one rule per user per path family (the web view, `/user/*`, `/hooks/<their hook>`) — and reloads Oathkeeper. The edge learns a new yard's address from that file, not from a call. Losing: Oathkeeper's API-managed rules — the rules then live in API memory with their own backup story, and a `diff` of the file no longer says who reaches what.

The customer is their yard's operator user, as in 8wxe 3.3 item 2: the commands that are "the operator's" are run by the provisioner, at provisioning, or by the customer in conversation with their yardmaster where a command exists for it.

### 3.3. The image: one Dockerfile, a volume for home

`image/Dockerfile`, on Debian: yardr, herdr, git and the harnesses, each pinned by version, with a `versions.txt` the provisioner prints into the trial note. The yard's home is a named volume mounted at the yard user's home, so deleting the container deletes nothing of the user's. Backup is a nightly `tar` of each volume to the Mac's disk, kept seven days; restore is a volume made from a tarball and a fresh container over it. Upgrade is rebuild and recreate: a new image is built, each container is stopped and started again from it over the same volume, and the server and daemon take the new binary up themselves while the web view starts fresh with the container. Losing: baking the home into the image — a build per user and no upgrade path; losing for backup: copying the live store file by file — a torn `yard.db` instead of one atomic tarball.

### 3.4. No systemd in the container: the entrypoint runs the yard

`server-yard` writes systemd user units and turns linger on, and a container has neither. So the container's entrypoint does the same steps without them: `yardr init --harness <kind> --operator <name>` if the volume holds no yard yet, then `yardr up` (herdr's server, the yard's server, the daemon), then `yardr web serve --unsafe --addr <container address>:8791 --host <the user's edge name> --as <user>` in the foreground. Docker's restart policy brings the container back after a reboot, which is linger's job done by the host. The entrypoint is idempotent like the script: a second start over a volume that holds a yard starts it, and makes nothing twice. No yardr change: every command it runs exists.

### 3.5. The harness login with no terminal: the yardmaster relays the led login

Two options, as the bead frames them: a terminal in the browser on `herdr agent attach <yard>-yardmaster` behind Oathkeeper, or the yard waiting for the login to appear while the person answers the harness's own flow elsewhere. This design takes the second: the yardmaster runs `yardr harness login <kind> --json` in its pane and relays each need — `choose` as buttons, `open_url` as a link, `input` as a field — through the conversation, in the web talk page or footplate, passing the answers back on the command's stdin. The person signs in through the harness's own flow (D2), the code is never stored, and an API-key user instead answers the key where the harness's flow asks for it. Losing: the browser terminal — a new pane-driving surface behind the edge, typing into panes yardr otherwise never types into, a larger build and a larger review for a trial. What the relay costs is an agent in the loop of a login: slow, and the trial writes down each place it stuck, which orders the later leading beads (2k0x B9 for the view, a footplate remote-login bead after it).

### 3.6. footplate's hosted mode: the edge token beside the yard entry, pairing as today

The person logs in at the edge from footplate: footplate opens the system browser on Hydra's authorize URL with PKCE, takes the redirect back, and keeps the token beside that yard's entry, in a 0600 file next to the device token, never in the settings file. On each call footplate sends both: the device token in `Authorization: Bearer`, exactly as today, and the Hydra token in a second header, `X-Edge-Token`, which Oathkeeper's `/user/*` rule reads (its `token_from`) and checks by introspection against Hydra, passing all headers on with the `noop` mutator. The yard reads `Authorization` and changes nothing. Pairing stays pairing by code as today (2.5): the token does not replace the device pairing. Losing: the Hydra token replacing the device pairing — the yard would have to verify the gateway's JWT on `/user/*`, the scpj shape built for a second surface, before anyone has tried the first.

What footplate shows on the first screen for a hosted yard: a fourth way, "Open a hosted yard", beside the three of 2.5. It asks for the edge address, then shows the hosted yard's row in three states: "Sign in at the edge" (opens the browser), "Pair this device" (the name and the code from the ready page), and, paired, the yard's conversations with the signed-in account named under the yard's name. A refused edge token and a refused device token are told apart on screen: the first offers sign-in again, the second offers pairing again, and neither is retried silently.

### 3.7. The trial: mckean signs up and lands one bead

mckean is the test user at the edge, as 8wxe 3.7 is one outside person for one week, except nothing here needs a terminal:

1. He signs up at the edge; the container comes up; the ready page shows the web view's address and the footplate pairing code.
2. He opens the web view in a browser and sees an empty yard acting as him; he pairs footplate as a device by the code.
3. He logs the harness in through the yardmaster's relay (3.5), subscription first; the feed, which showed the harness as not logged in, shows the yardmaster starting.
4. He answers the yardmaster's greeting — what the project is, what tests it — and gives the repository: one of his own, reached through the yard's GitHub pack with a token of his own typed in conversation (2.7: trial only, his own token, and the note says so).
5. He writes what he wants built; the yardmaster makes a bead and advances it; he picks where the yard asks, in the browser or footplate.
6. He sees the bead merged, and the commit on the repository.

What the trial writes down, as a note on the trial's bead: time from sign-up to the first merged bead; each time anything needed the service operator and for what (this orders the beads after the trial); each place the login relay stuck; how often the login expired; what the container cost for the week in machine weight and how much of the week it worked; the image's `versions.txt`; whether a yard for a person who did not build it is a thing they come back to.

### 3.8. Care

The risks are 8wxe 3.6's, narrowed to a container trial: another user's agent reads this user's code — the container is the wall, a kernel shared between users who run arbitrary code, named as the limit (D1); the edge's rules mistake one user for another — the view trusts `--as` for every request alike until rrce and 0hrq land, so a wrong rule is a breach, and the rules file is diffed and reviewed per change; a stolen footplate token pair reads the yard as the user — removal at the yard (`device rm`) and at Hydra ends it; the service operator reads the user's code — nothing technical stands against it, and it is said to the trial users plainly; the machine is lost — the repository is on GitHub, the store's backup is the nightly tarball, a login is made again.

## 4. Left out

- **Billing and metering.** The trial's users pay nothing and cost machine weight only; what a user costs is read from Docker's own accounting outside the yard. Money enters through the provisioner's records in `yardr-hosted`, never the yard.
- **Stopping idle yards.** A yard survives a stop, but a stopped container receives no webhook and fires no timer, so the router would have to wake it (8wxe 3.3 item 9). A later lever on cost, in the provisioner.
- **VMs, EC2, k8s.** The scaling wall after containers (D1); the image and the entrypoint move, the provisioner learns a second backend.
- **The GitHub App with broker and router.** The trial uses a token of the user's own (3.7); the App, the token broker and the webhook router are 8wxe 3.3 item 3, unchanged, in `yardr-hosted` when strangers arrive.
- **A brand and a landing page.** The trial's pages are the sign-up, the ready page and the yard itself; marketing is not a bead here.
- **A multi-tenant store.** Excluded by the premise: every customer gets an isolated yard of their own, and yardr is not made multi-tenant (8wxe 1).
- **A sandbox inside yardr.** The container is the wall; the harness's own permission modes are what a group chooses from. Same exclusion as 8wxe's.
- **The service collecting any credential.** D2: the harness's own flow, never a paste field of the service's.
- **A hosted secret handoff over the edge.** `GiveSecret` from a device holds to the yard's own machine (2.7); whether the gateway's network joins that trust, or the handoff travels another way, is its own design after the trial. The trial types one token in conversation and says so.
- **Self-service re-pairing.** During the trial the service operator re-opens a lapsed pairing (3.2); a user doing it themselves — a button on the ready page, or a narrow capability the yardmaster may use — waits for the trial's note on how often it was needed.

## 5. Open questions

1. **What are the sign-up and login pages?** Kratos is API-only and needs a UI (2.9). (a) Ory's ready-made account pages, hosted beside Kratos: the least code, the least control over what the person sees. (b) A small page of the service's in `yardr-hosted`, calling Kratos's API flows: the trial's own words, at the cost of owning registration, login, recovery and every edge of each. I would take (a) for the trial: the trial learns about yards, not about forms.
2. **Does Hydra ask the person to consent to footplate?** (a) Auto-approve the first-party client: sign in and the token appears, no screen, at the cost of no moment where the person sees what footplate may do as them. (b) A consent screen naming footplate's access: the honest moment, at the cost of building the consent page Hydra needs anyway and one more step before the first pairing. I would take (a) for the trial: the person installed footplate themselves, and the pairing code is already their explicit grant to that device.
3. **Who may sign up for the trial?** (a) Anyone with the address: the truest test, and strangers' code on a shared kernel (D1) from day one. (b) An invite list the provisioner checks — one list of emails or codes, no other machinery: people mckean knows, matching the wall's stated limit, at the cost of one more thing the operator tends by hand. I would take (b): the trial's wall is named for people he knows, and the sign-up should say the same.

## 6. The beads it would become

In the order they would land, across three depots: H in `yardr-hosted` (made by H0), Y in `yardr`, F in `footplate`. H5, F1 and F2 can run beside each other once H4 stands; everything else lands in the order below. Each yardr bead passes `sh .yardr/test`; each footplate bead passes its depot's gate (`cargo fmt`, clippy, test, build, reviewed twice, the second for security alone); each `yardr-hosted` bead passes the gate H0 sets. Anything that starts servers is tried on loopback or in an isolated yard, never mckean's own.

**H0. The `yardr-hosted` repository, a depot of this yard.** Goal: the repository exists with its layout (`image/`, `edge/`, `provision/`, `docs/`), a README saying what the service is and is not (D1–D3 in a paragraph each), and a test gate a builder can run (`sh .yardr/test` if the depot is git-backed with one, else the documented equivalent); it is added as a depot of this yard so later beads route. Where to look: 8wxe Q5; `yardr depot add` and the depot's `.yardr/depot.md` convention. Done when: the depot lists, its gate runs clean on an empty tree, and H1's bead has a directory to land in. Depends on nothing, and on open question 3 for the invite list's first entry.

**H1. The yard image.** Goal: `image/Dockerfile` on Debian with yardr, herdr, git and the harnesses pinned in `versions.txt`; the yard user's home a named volume; the entrypoint of 3.4 (init if no yard, `up`, the web view in the foreground), idempotent; a README with build, run and shell-into-container for the operator. Where to look: contrib/server-yard/server-yard for the steps the entrypoint mirrors without systemd; `yardr up` (internal/cli/updown.go); the web flags in internal/cli/web.go. Done when: from a bare checkout the image builds; a container over a fresh volume ends with `yardr doctor` naming no fault but the harness login; a second start over the same volume starts the same yard and makes nothing twice; after a container restart the yard comes back unaided. Depends on H0.

**H2. Provision one yard from the map.** Goal: `provision/provision <user>` — the map row in SQLite, the volume, the container from H1's image with `--operator <user>`, the web view on the docker network with `--host`, the device pairing opened and its code written to the hosted store for the ready page, the Oathkeeper rules file regenerated and reloaded. Where to look: H1's README; internal/cli/devicepair.go (`--json` for the code); the Oathkeeper rules file format as Ory documents it. Done when: one command provisions a yard for a test user end to end; the rules file diff shows exactly that user's rules; deleting the test user removes the container and its rules and keeps the volume; run twice for one user it changes nothing the second time. Depends on H1.

**H3. The edge: Kratos, Oathkeeper and the sign-up on the Mac.** Goal: `edge/compose.yaml` with Kratos (plus its database), Oathkeeper with the rules file H2 regenerates, the sign-up and login pages (open question 1), the ready page showing the web view's address and the pairing code, and the public address through the existing tunnel; the invite list if open question 3 answers so. Where to look: xpf8 2.7 for what each Ory piece does and needs; the tunnel that serves signalbox.yardr.dev for the public road; H2's rules file. Done when: a sign-up in a browser ends on the ready page with a working container behind it; Oathkeeper admits the user to their view and refuses them elsewhere; a second user reaches only their own; Kratos's sessions end where its UI says they do. Depends on H2.

**H4. Hydra and footplate's token.** Goal: Hydra in the compose file with its login (Kratos) and consent application (open question 2), the Oathkeeper `/user/*` rule checking `X-Edge-Token` by introspection and passing headers on, and a documented PKCE flow a client can follow by hand with curl. Where to look: xpf8 2.7 (Hydra needs a login and consent app; Oathkeeper's `oauth2_introspection` and `noop`); section 3.6 for the header contract. Done when: a PKCE flow in a browser yields a token that passes the `/user/*` rule and reaches a test container, and a bad one is refused at the edge with the yard never touched; revocation at Hydra refuses within the introspection cache's TTL, stated in the README. Depends on H3.

**F1. footplate sends the edge token beside the device token.** Goal: a yard entry keeps an edge token file (0600, beside the device token, never in settings), sends it as `X-Edge-Token` on every call to a yard at an address, and tells a refused edge token from a refused device token on screen, each with its own next step and neither retried silently. Where to look: `src/api.rs` (the whole client), `src/settings.rs`, `src/fake.rs` for a fake yard on TCP, the "401 is a state of its own" pattern. Done when: against the fake yard plus a stand-in edge, a good pair reaches the yard, a bad edge token offers sign-in again, a bad device token offers pairing again, and an `http://` address off loopback is still refused; tokens live in 0600 files only. Depends on H4.

**F2. footplate's hosted first screen.** Goal: the fourth way of 3.6 — "Open a hosted yard": edge address, PKCE sign-in in the system browser with the redirect taken back, token kept as in F1, then the pair-by-code road as today, then the yard's conversations with the signed-in account under the yard's name. Where to look: the first screen and the pair-with-an-address road in footplate's README and `src/settings.rs`; H4's PKCE flow. Done when: from no yard, the hosted road ends in the yard's conversations with no terminal and no pasted token; sign-in and pairing failures say which failed and what to do; the security review holds footplate's run-table with the browser round-trip added. Depends on F1.

**H5. Backup and upgrade.** Goal: nightly tarballs of each volume to the Mac's disk kept seven days, a restore to a fresh volume and container, and the rebuild-and-recreate upgrade of 3.3, each as one command with a dry run that says what it would do. Where to look: H1's image and volume layout; the map in H2 for the volume list. Done when: a backup, a volume deleted, and a restore bring a test yard back with its beads and mail; an upgrade over a test yard keeps the store and takes the new binary up; the trial's note can quote both commands. Depends on H2.

**T. The trial of 3.7.** Goal: mckean signs up as a test user and lands one bead, writing down what 3.7 lists. A person's, not a builder's; the yardmaster keeps the record. Depends on H5 and F2.

**Y1. The yard verifies the gateway's JWT (yardr-rrce, filed).** The scpj bead 2, unchanged: `web_jwt_*` settings, bounded JWKS cache, per-request verification with the decided codes, landing after the trial as the hardening that makes an edge mistake a refusal instead of a breach. Depends on T.

**Y2. The verified user acts (yardr-0hrq, filed).** The scpj bead 3, unchanged: per-request identity through talk, asks, moves, needs queue and CSP. Depends on Y1.

Proposed for beads that exist, not made here: yardr-aj24 (the ssh trial of 8wxe 3.7) is superseded by T once this design lands — the yardmaster closes it as such, since no terminal trial runs now; yardr-rrce and yardr-0hrq keep their bodies and gain this design as the reason their order matters.
