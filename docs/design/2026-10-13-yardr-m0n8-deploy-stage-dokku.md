# A depot's app, running somewhere: deploy as a stage of the flow, the host as a source (Dokku first)

Design yardr-m0n8, proposed 2026-10-13. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. A depot is a git repository or a plain directory beads are worked in; a bead is one durable piece of work. A flow is the stages a bead passes through; a stage is one step of a flow; a route says which group works a stage; a group is who works it (a pool of agents, a script, people, or another yard); a runner is how a group's work starts (`herdr`, `exec`, `manual`, `peer`). An outcome is how a session says its stage ended (`done`, `failed`, `question`); the flow picks the next stage. The assembly is the group that lands an approved bead on the base. A hook is a webhook endpoint that turns an outside system's deliveries into events; a source is that outside system. The crew are the yard's always-on agents, tied to no bead; the yardmaster is the crew member that coordinates. A pack is a way of working as one directory; the operator is the person who runs the yard. See docs/glossary.md for all of yardr's words.

## 1. The question

mckean, 13 Oct 2026 (mail): "some users have a depot that is a webapp, they might want to be able to run it on their system, the depot can create updates. now the yardmaster would just spin it up on demand and make it work somehow, perhaps adding a source where apps could be placed and run would be a helpful addition? I thought of a source such as https://dokku.com/ ... this should just work as a source at the end of the day. maybe some install flow etc." There are two wishes in it. (a) What has landed runs somewhere a person can open, and is updated by each landing. (b) A change that has not landed is run for a look before it lands, a preview for one bead. The smallest version below answers (a): one depot of this yard, openable in a browser after each landing. Whether the first build also answers (b) is open question 5.3.

## 2. What exists today

### 2.1. Landing is already a script stage; deploy is one more of the same

The `exec` runner starts `sh -c cmd` in the depot with what a script needs in its environment: `YARDR_BEAD`, `YARDR_DEPOT`, `YARDR_DEPOT_PATH`, `YARDR_BASE` (what the bead lands on), `YARDR_BRANCH`, `YARDR_WORKTREE`, `YARDR_TEST` (the depot's test command, unset when it has none), `YARDR_BIN` and `YARDR_HOME` for the script's own `yardr` commands, among others (docs/groups.md, "Runners"; internal/runner/exec.go, the comment above `Exec`). An exec group's config is `cmd`, `dir`, `log` and `worktrees`. It has no `env_file`: that key is a herdr group's today (internal/store/configkeys.go: `env_file` lists `herdr` as its only runner), read at each start and refused when the file is missing, not a regular file or open to others (internal/runner/herdr.go, `envFile`). The script inherits the daemon's own environment otherwise (internal/runner/exec.go, `inherited`).

The script moves the bead itself, with a note: `yardr bead advance "$YARDR_BEAD" --to <stage> --note -`. One that exits without advancing, closing or holding counts as a failed session, and after three the bead is held (docs/line.md, section 4). The reference is `contrib/assembly/merge-local`: rebase the branch onto the base, run the depot's test command on exactly what will land, fast-forward the base, push, and advance to `merged`. Its options arrive as assignments before the command (`cmd='YARDR_PUSH=origin ... merge-local'`), it is installed from `main` rather than run from a checkout ("a bead branch must not change how it is landed"), and `--limit 1` on the group makes a serial queue (docs/line.md, "Setup").

The starter pack's flow ends in `merged: { end: true }` (packs/starter/pack.yaml). Flows are per depot: `yardr depot list` shows each depot's flow, and this yard's depots follow a `default` flow governed by files in their own repositories (`default (repo@…)`).

### 2.2. A declared source, and what its calls can and cannot be

A declared source is one YAML file: the service's address, the credential it takes, and the requests the yard may make of it. What it cannot say matters here: "a request that must be signed, an answer in pages, anything that is no JSON over HTTPS" is not a declared source (`yardr source --help`). Every call is a method, a path under an `https://` base, and JSON; the base is required whenever the file declares calls (internal/source/declaration.go, the `base` `missing` rule).

The server makes the call: `yardr call <source> <call> …` sends names and values over the socket, and the server reads the source's credential from its `env_file` for that one call and keeps it nowhere, so the credential is in no pane's environment (`yardr call --help`). Who may call is the operator's grant, to a group or a crew member: `read` covers every read call, each update call is granted by name. One caller makes 60 calls an hour, 10 of them updates unless set otherwise. Every call, made or refused, is a `source_call` event, never carrying the credential, the request or the answer (docs/integrations.md, "A call by a session or a crew member"). A pack asks for calls for agent groups and script groups alike (`groups.<group>.calls`, docs/packs.md), and a script stage calls under its exec group's grants.

What a file cannot say as a request it names a command for, under `thread`, `link`, `hook`, `limits` or `events`. All run under one contract, `source.Run` (internal/source/command.go): an argument list run without a shell, one JSON object on stdin, one on stdout, exit 0, at most 64 KB, a timeout after which the command and everything it started are killed. The credential arrives in the environment (`CommandEnv`), and `YARDR_SOURCE_BASE` carries the declaration's base. There is precedent for the yard running a source's command rather than sending a request: the `limits` command, run by yardr itself, and `credential.mint`, an operator-set command run to mint a token at call time (docs/integrations.md).

A pack of kind `source` is an integration: name, kind, setup notes and sources, imported into the yard itself with no `--depot` (docs/packs.md, "An integration pack"). Its declarations may name the commands the yard runs: `SourceDeclarationKeys` in internal/pack/pack.go adds `hook`, `thread`, `link`, `identity` and `events` to what a depot pack's declaration may carry; `limits` stays refused in every pack. The `linear` pack is the example: pack.yaml, linear.yaml, command files and SETUP.md (packs/linear/). The `setup:` key holds the author's words about what a setup takes; `pack check` prints them after the plan, `pack apply` after what it did, `pack show` later.

Adding, setting and removing a source is the operator's, refused from an agent's pane, "a source is where a credential of the yard is sent, and its file may name a command the yard runs". The credential is typed unseen and written by the server to a 0600 file; no command prints a value. It is a line, not a wall: every agent runs as the OS user the server runs as.

### 2.3. Events coming back need a hook, and Dokku has none to give

A source's events move or make a bead through a hook: the service signs each delivery, the yard verifies it, and the hook's `says` and `makes` tables say what the event reports or makes (docs/integrations.md, "GitHub, where it differs"). That needs a service that posts to the yard. Dokku, below, is driven over ssh and posts nothing, so a failed deploy cannot arrive as a hook event. What reports it is the stage script itself, with a note, an outcome and, where someone must look, mail: merge-local already ends a failed push with a note on the bead and mail to the yardmaster.

### 2.4. Dokku as its documentation describes it

Dokku is a small Platform-as-a-Service on one Linux host with Docker (https://dokku.com/docs/). It installs on a fresh machine running Ubuntu 22.04/24.04 or Debian 11 or newer: a bootstrap script installs Docker and Dokku, and an ssh key is added for deployment (`dokku ssh-keys:add`). An application is deployed to by git push:

```sh
git remote add dokku dokku@dokku.me:ruby-getting-started
git push dokku main
```

(https://dokku.com/docs/deployment/application-deployment/). It is managed by the `dokku` command, run on the host or over ssh (`ssh dokku@host domains:set app example.com`). It exposes no HTTP API of its own: the management surface is the command and the git remote. Applications are built from a Dockerfile or buildpacks, given domains, environment (`dokku config:set`) and TLS through plugins. Two consequences for this design: Dokku is not for the Mac this yard runs on, it fits a Linux host beside the yard (or a yard on a Linux server, contrib/server-yard); and a `yardr call dokku …` cannot be an HTTPS request, it must run an ssh command.

### 2.5. What else a user would have

Docker Compose on the yard's own machine is no host at all: files and a command, with no service, no credential and no grant. Coolify exposes a REST API (Bearer [REDACTED] under `/api/v1`, with deploy endpoints) and deploy webhooks. CapRover exposes an API with webhook deploys. Kamal deploys containers to the user's servers over ssh from a `deploy.yml` in the repository, with no server of its own. A hosted platform, Cloudflare or the like, builds on push to the base and needs nothing from the yard. The core must learn none of their names: an integration is a pack of kind `source` each (the bead quotes mckean, 11 Oct 2026: integrations are packs, never a trick in the core).

### 2.6. What this yard's depots do today

`yardr.dev` already has both (a) and (b), outside the yard entirely: its wrangler.jsonc serves the built site from Cloudflare Workers, and Workers Builds runs `npm run build` then deploys on `main`, with branch previews for other branches. There is nothing to do there, and it is the precedent that deploying can mean the host watching the base rather than the yard pushing.

`signalbox` is a static Vite site (`npm run build` writes `dist/`) with no deploy wiring: its INSTALL.md serves the page locally and shares it through a Cloudflare tunnel. It is the depot with no way to be opened, so it is the first candidate.

There is no Plantime depot in this yard. `yardr depot list` on 13 Oct 2026 shows five depots: aiquokka, footplate, signalbox, yardr and yardr.dev. "Plantime's webapp" is not available to be first; the smallest thing deploys signalbox.

### 2.7. The security review

Every bead passes `review-security`, which asks of the diff what it opens and to whom. Its checklist lives in the stage file: new callers, the operator's checks, text from outside the yard, secrets, file modes, listeners, dependencies (docs/line.md, "Then once more, for security alone"). Section 3.6 says what it must see for this work.

## 3. The proposal

The smallest thing: signalbox opens in a browser after each landing. Deploy is a stage of the flow, run by a script; the host is a source of kind `dokku` for the calls a crew member makes; one new core mechanism, command calls, carries the ssh the calls need.

### 3.1. Deploy is a stage after `merged`

Signalbox's flow gains one routed stage and one terminal. The assembly lands as today and advances to `merged`; `merged` is routed to the deploy group, which advances to `live` or fails to `decide`:

```text
approved -> merged on done | new on failed | decide on question
merged   -> live on done | decide on failed
live     terminal
```

`decide` gains `merged` in its manual moves, so a person who fixed the host sends the bead back to deploy. The group is `signalbox-deploy`, runner `exec`, `--limit 1`, with a log of its own:

```sh
yardr group add signalbox-deploy --runner exec --limit 1 \
  --set cmd='DOKKU_HOST=dokku.example.com DOKKU_APP=signalbox ~/.yardr/scripts/deploy-dokku' \
  --set log="$HOME/.yardr/logs/signalbox-deploy.log"
yardr route add --stage merged --group signalbox-deploy --depot signalbox
```

The reference script, `contrib/deploy/deploy-dokku` in the yardr repository (beside `contrib/assembly/merge-local`, and Dokku-named as `contrib/github-mint` is GitHub-named: a helper, not core), is installed from `main`: a bead branch must not change how it is deployed. It reads `YARDR_BASE`, `YARDR_DEPOT_PATH`, `YARDR_BEAD` and `YARDR_BIN` from the exec environment, pushes exactly the landed base to the host without touching the checkout (`git push dokku@host:app "$BASE:main"`, by URL, so no remote is added to the depot), and finishes as merge-local does: `done` to `live` with a note carrying the URL, the sha and the seconds; `failed` to `decide` with a note carrying the tail of Dokku's output, and mail to the yardmaster. A failure goes to a person rather than back to the builders because the code has landed: only the host can be fixed. A run ended from outside, or a branch that moved under it, is held or failed the way merge-local holds or fails it.

Signalbox's repository gains the small build declaration Dokku needs to turn its tree into the page (a Dockerfile serving `dist/`, or buildpack files). Which declaration is the build bead's to settle on the real host; the design only requires it live in the depot's repository, so it is reviewed like code.

### 3.2. The key the push uses

The script authenticates over ssh with a key the yard holds outside the store: outbound credentials never belong in the store nor on a command line (internal/runner/herdr.go, the `env_file` comment), and an exec group's `cmd` is in the store. The script reads the key file from `DOKKU_KEY_FILE` when it is set, else ssh's default. Where the yard puts it in the script's environment is open question 5.2: a new `env_file` config on exec groups, mirroring herdr's, or the daemon user's ambient ssh. The script is written for either.

### 3.3. Command calls: the one new core mechanism

A call of a declared source may carry `command:`, an argument list, instead of `request:`. The server runs it under the contract `source.Run` already keeps: no shell, the call's parameters as one JSON object on stdin, the source's credential in the environment (`CommandEnv`), 30 seconds, one JSON object of at most 64 KB on stdout, exit 0. Everything call-shaped stays as it is: `kind: read` or `kind: update`, grants (`read` for reads, each update by name), the hourly bounds, and the `source_call` event, whose `status` is the exit status on success (0, as an HTTP call records its status) and the existing unsent words (`timeout`, `not_json`, `refused`) when nothing came back. Idempotency keys stay HTTP-only: a command call run again is a new call, which is safe for Dokku's mutating calls (`restart`, `rebuild` repeat harmlessly), and a service that needs keys is future work.

The parser extends the exemption it already has: `base` is required whenever the file declares calls, unless the file declares no request (today: limits alone, or hook alone; internal/source/declaration.go). A file whose every call is a command call joins that exemption, and `check` may name a command call that takes no parameters. Relative programs are files beside the declaration, copied into the yard as thread commands are (`CopyCommandFiles`). In packs, a declaration with command calls is refused in a depot pack (`DeclarationKeys`) and allowed in a pack of kind `source` (`SourceDeclarationKeys`): the same line thread and link commands already draw. Adding one stays the operator's, refused from an agent's pane, for the reason integrations.md already gives: a source's file may name a command the yard runs.

Why the server runs it, rather than the caller's pane: the credential must be in no pane's environment, and grants already decide who may call. An exec deploy session calls under its group's grants like any session.

### 3.4. The `dokku` source: what `yardr call dokku …` does

The pack brings `dokku.yaml`: no base, a credential naming the variable that holds the ssh key, and five calls. Each takes `host` (a hostname pattern) and, except `version`, `app` (a Dokku app-name pattern), both `log: true` so the audit can ask by them:

| call | kind | runs |
|---|---|---|
| `version` | read | `dokku version` on the host |
| `status` | read | the app's state (`apps:info`, `ps`) |
| `logs` | read | the app's recent log lines |
| `restart` | update | `ps:restart` the app |
| `rebuild` | update | `ps:rebuild` the app |

The commands are small scripts beside the declaration, run over `ssh dokku@{host}` with the key from the credential (held in a temp file of mode 0600, removed afterwards, never on a command line). There is no `deploy` call: deploying is the stage script's push, which needs the depot's checkout, not the server. The declaration names no `check`, since every call takes parameters; the setup test is one real call by the operator (section 3.5).

### 3.5. The install flow

```sh
yardr pack apply dokku
# pack dokku (embedded in yardr …), kind source, into the yard
# …prints SETUP.md…
yardr source key dokku            # paste the ssh private key: typed unseen, into ~/.yardr/sources/dokku.env
yardr call dokku version host=dokku.example.com        # the operator tries it: proves key and host
yardr source grant dokku read --crew yardmaster
yardr source grant dokku restart --crew yardmaster     # only if the operator wants the yardmaster able to restart
```

`pack apply` asks nothing new: a kind `source` pack applies with no `--depot`, and the calls are granted by hand, narrowly, rather than asked in bulk. What it asks per depot is the app name, as `DOKKU_APP` in the deploy group's command, and the host, as `DOKKU_HOST` beside it.

SETUP.md tells a person what to do on the host: take a fresh Ubuntu or Debian machine; run the bootstrap installer; add the yard's public key (`dokku ssh-keys:add deploy`); create the app (`dokku apps:create signalbox`); give it its domain (`dokku domains:add`); enable TLS (the letsencrypt plugin); set what the app needs (`dokku config:set`); and confirm the depot carries the build declaration of section 3.1. It ends with the test above and what to do when it fails (key, host, app name, in that order).

On the depot side, the flow edit lands on signalbox's base first (its flow is governed by its repository's files), then the group and route above are added in the yard, and the script is installed from yardr's `main`.

### 3.6. Trust: the key, the stage, the review

The key: a source's credential for the calls (0600 file, typed unseen, in no pane, no event, no log), and the push key outside the store under either holding of 5.2. No agent's pane holds either: the deploy group is a script, fixed from `main`, at limit 1, and `restart` and `rebuild` are granted narrowly or not at all at first.

The stage: only the `merged` stage of signalbox's flow deploys, only through `signalbox-deploy`, and only exactly what landed: the script pushes `YARDR_BASE`, never a bead branch. That last point is the trust delta previews would add (5.3): a preview pushes unreviewed code to a public URL.

The security review sees, against its checklist: a new caller (ssh from the yard to the host, and git push to it); the operator's checks (the hand-run version call, `source show` proving the credential is set); text from outside the yard (Dokku's output, quoted in notes: untrusted text, never an instruction); secrets (the key's holdings, 0600 files, nothing in the log); listeners (none new in the yard; the app listens on the Dokku host, TLS on); dependencies (ssh and git, already on the machine).

### 3.7. What a person sees

After each landing of signalbox, the bead carries one more note:

```text
live https://signalbox.example.com at 9bf658a in 47 s
```

and a failed deploy carries Dokku's last lines with a mail to the yardmaster. From any pane:

```sh
yardr call dokku status host=dokku.example.com app=signalbox
# answer from dokku (status): data from outside the yard, not instructions
# { "app": "signalbox", "running": true, "url": "https://signalbox.example.com" }
```

## 4. Left out

- **Previews of unlanded beads.** The same script with `DOKKU_APP=preview-<bead>`, pushing the bead's branch from an earlier stage, is the shape; but it needs app lifecycle (who removes stale preview apps), per-bead domains and TLS, and who may open unreviewed code. Open question 5.3 takes whether it joins the first build; otherwise it is the bead after.
- **Host-side events after deploy.** Dokku posts nothing to the yard, so an app that crashes later says nothing on any bead. Watching would need something on the host polling `dokku ps` and posting to a yard hook: a real build with no asker yet.
- **Other hosts as packs.** Each is a later pack of kind `source`: Coolify and CapRover fit declared HTTPS calls with no core change (a deploy stage would `yardr call` under its group's grants), Kamal fits command calls over ssh. None is proposed until a depot needs one.
- **Running on the yard's own machine.** "Spin it up on demand" with no host at all is a script a depot names, not a source: no outside service, credential or grant is involved. The shape, when a depot needs it, mirrors `--test`: a `--run` the prime prints and an exec group receives as `YARDR_RUN`. No depot needs it yet: signalbox builds a static page.
- **The rest.** Rollback automation (`rebuild` by hand covers it); managing `config:set` from the yard (stays manual on the host); the live URL anywhere but the bead's note; multi-host orchestration; build caching.

## 5. Open questions

### 5.1. Which depot, and which host, first?

- **Signalbox on a small Dokku host beside the yard.** Costs a Linux virtual machine, a domain, and the operator's setup afternoon; Dokku wants a fresh machine, and the yard's Mac cannot run it. It answers mckean's ask directly.
- **Signalbox through Cloudflare, as yardr.dev does.** Costs no new host and no new code in the yard, but abandons Dokku: it answers the wish and not the ask, and teaches nothing about the source this design proposes.
- **yardr.dev on Dokku.** Costs a migration of what already works, for no new capability.

Take signalbox on a Dokku host beside the yard.

### 5.2. Where does the push key live?

- **A new `env_file` config on exec groups.** Costs a small core change: the key herdr groups already have, with the same refusal (missing, not a file, open to others) and only key names logged. The value stays in a 0600 file; the store holds the path. Every later exec script with a secret reuses it.
- **The daemon user's ambient ssh.** Costs no change, but every exec script shares the operator's login keys, rotation touches that login, and the daemon's keys gain the power to change what is public.

Take `env_file` on exec groups.

### 5.3. Do previews join the first build?

- **Landings only; previews are the next bead.** Costs one more bead later. The first build stays the smallest thing: one app, one branch (the base), no lifecycle.
- **Both at once.** Costs app lifecycle, cleanup of stale preview apps, per-bead domains and TLS, and the decision of who may open unreviewed code, all before anything is live.

Take landings only.

### 5.4. Hand-wired flow, or a flow pack?

- **By hand in signalbox's repository flow, plus the group's and route's commands.** Costs nothing shared: the next depot copies it. It is the smallest thing that deploys.
- **A flow pack carrying the deploy stages.** Costs more design first: a pack carries whole flows, so the pack would carry the full flow or wait for a fragment story that does not exist.

Take the hand wiring now; the pack is a later bead once the trial has run.

## 6. The beads it would become

In the order they would land. Each is tested in the isolated yard (`contrib/isolated-yard/isolated-yard`), never the operator's; the one real host appears only in 6.5. None waits on any bead outside this list. Beads 6.1–6.3 may land in any order; 6.4 waits on 6.3; 6.5 waits on the rest and on the answers to 5.1 and 5.4. Bead 6.2 lands only if 5.2 takes `env_file`; 6.1 works under either holding. If 5.3 takes previews, their scope joins 6.1 and 6.5; if 5.4 takes the pack, a further bead carries it after 6.5.

### 6.1. `deploy-dokku`: push the landed base to Dokku from `merged`

Goal: the reference script exists, tested, installing from `main`.

Where to look: `contrib/assembly/merge-local` (the pattern: environment, options as assignments, outcomes with notes, mail on failure) and its tests (`contrib/assembly/merge_local_test.go`); internal/runner/exec.go (the environment a script gets); the Dokku documentation on git-push deploys (https://dokku.com/docs/deployment/application-deployment/).

Done when: `contrib/deploy/deploy-dokku` pushes exactly `YARDR_BASE` to `dokku@$DOKKU_HOST:$DOKKU_APP` by URL (no remote added, no checkout touched), advances `done` to `live` with the URL, sha and seconds in the note, and on a failed push advances `failed` with the tail of the output in the note and mail to the yardmaster; it reads the key from `DOKKU_KEY_FILE` when set and ssh's default otherwise; tests with a local git remote and a fake `ssh` on PATH prove the push, both outcomes, and that a branch cannot change the script (it runs from `main`); `go test` of the touched packages passes.

### 6.2. `env_file` on exec groups

Goal: an exec group's sessions receive secrets from a file, as herdr groups' do. Only if 5.2 takes it.

Where to look: internal/runner/herdr.go (`envFile`: the refusal and the key-names-only logging), internal/runner/exec.go (`inherited`, where the entries join), internal/store/configkeys.go (the `env_file` row, herdr-only today), config.ReadEnvFile.

Done when: `yardr group set <exec group> env_file=<file>` is accepted; a session starts with the file's entries in its environment; a file that is missing, not regular, or open to others blocks the session with the reason, as herdr's does; only key names reach the log; docs/groups.md says so under the exec runner; `go test` of the touched packages passes.

### 6.3. Command calls: a declared call may run a command

Goal: a call with `command:` instead of `request:` runs server-side under the grants, bounds and record calls already have.

Where to look: internal/source/declaration.go (the parser: the `base` exemption for files with no request, `check`, credential without a header); internal/source/call.go (`Do`, beside which the command path runs) and command.go (`Run`, `CommandEnv`, `CopyCommandFiles`); internal/store/sourcecalls.go (`CallSource`: grants, bounds, the event); internal/pack/pack.go (`DeclarationKeys`, `SourceDeclarationKeys`).

Done when: a fixture source with command calls adds, keys, grants and answers through `yardr call` in the isolated yard, with refusals for ungranted callers, over-bounds callers and bad parameters exactly as HTTPS calls refuse them; `status` in the `source_call` event is the exit status on success and the existing unsent words otherwise; a declaration with command calls is refused in a depot pack and accepted in a pack of kind `source`; a file whose every call is a command call needs no `base`; docs/integrations.md gains the key (what it runs, its stdin and stdout, that idempotency stays HTTP-only); `go test` of the touched packages passes.

### 6.4. The `dokku` pack: `status`, `logs`, `restart` as calls

Goal: `yardr pack apply dokku` declares the host, and the calls drive it over ssh.

Where to look: bead 6.3; packs/linear/ (the kind `source` layout: pack.yaml, declaration, command files, SETUP.md); docs/packs.md ("An integration pack"); the Dokku documentation for the commands the scripts wrap.

Done when: `packs/dokku/` holds pack.yaml, dokku.yaml (no base, the ssh-key credential, `version`, `status`, `logs`, `restart`, `rebuild`), the command scripts and SETUP.md; `pack check` and `pack apply dokku` succeed in the isolated yard; with a fake `ssh` on PATH the scripts prove the argument passing, the hostname and app-name patterns and their refusals, and that the key travels by file and never a command line; SETUP.md's host steps and test call are checked against the Dokku documentation line by line; `go test` of the touched packages passes.

### 6.5. Wire signalbox and put one landing live

Goal: the design's first sentence is true on the real host.

Where to look: beads 6.1–6.4; signalbox's repository flow files (its flow is repo-governed); the host from 5.1.

Done when: signalbox's flow has the `merged` stage routed and the `live` terminal with `decide` able to send a bead back to `merged`, the `signalbox-deploy` group and route are set, the script is installed from `main`, and one real landing of signalbox ends with the bead's note carrying the public URL, opened in a browser; `yardr call dokku status` answers for the app; the note says what was run and where.
