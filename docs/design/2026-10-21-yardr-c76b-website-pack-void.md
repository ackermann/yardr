# A website, built by the yard and deployed to the user's own Void platform: a `website` pack with a deploy stage

Design yardr-c76b, proposed 2026-10-21. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. A depot is a git repository or a plain directory beads are worked in; a bead is one durable piece of work. A flow is the stages a bead passes through; a route says which group works a stage; a group is who works it (agents, a script, or people); an `exec` group runs a script. An outcome is how a session says its stage ended (`done`, `failed`, `question`); the flow picks the next stage. The assembly is the group that lands an approved bead on the base. A pack is a way of working as one directory, imported into a depot with `yardr pack apply`; the starter pack is the one a new yard begins with. The operator is the person who runs the yard. See docs/glossary.md for all of yardr's words.

Void (https://void.cloud) is a framework and deployment CLI for Cloudflare Workers. A Void platform is Void's own deployment service, installed by its owner into their own Cloudflare account; "self-hosted" in this document means such a platform. A project is one app on a platform; a project deploy token is a credential that may deploy one project and nothing else.

## 1. The question

mckean, 20 Oct 2026 (mail 52500, as the bead quotes it): a starter variant "for someone who wants to use yardr to build their website and deploy it, all self-hosted". The proposed shape: the starter flow plus a deploy stage after landing that runs `void deploy`, authenticated by `VOID_TOKEN`, recording the URL on the bead, with `void project rollback` as the way back. And from note 7920 (yardmaster, 20 Oct, mckean's answer in mail 52507): "The Cloudflare token lives only in the self-hosted Void install; the yard holds the Void token." This design settles the vehicle, the deploy stage, the credential, the URL, the way back, what the user sets up, and the first trial.

## 2. What exists today

### 2.1. Deploying from `merged` is built and has run live

The bead says m0n8's deploy stage "was never built, so this is the first deploy stage". That is no longer true, and this design rests on what was built:

- `contrib/deploy/deploy-dokku` (beads yardr-rby9, yardr-v4j7, yardr-at4k; previews in yardr-c84g) is the reference deploy script, 463 lines, documented in contrib/deploy/README.md. It runs as the `cmd` of an exec group with `--limit 1`, routed for `merged`; it resolves `YARDR_BASE` to its commit as it runs, deploys that commit, and advances the bead `done` to `live` with the note `live <url> at <sha> in <n> s`, or `failed` to `decide` with the last 100 lines of output marked as text from the host, and mail to the yardmaster. Deploys coalesce (the base as it stands is deployed, not the bead's own commit). A child of a train deploys nothing and goes on to `live` with a note naming the train. A run ended by a signal holds the bead.
- signalbox's flow (its repository's `.yardr/flows/default.flow`) carries the edge m0n8 section 3.1 proposed: `merged -> live on done | decide on failed`, `live terminal`, `decide -> backlog | new | merged` (bead signalbox-httz).
- The trial ran on 13 Oct 2026 (signalbox-24sp, note 3598): signalbox-x8zp landed adc7a9e, was pushed and built in 27 s, and its note read `live http://signalbox.192.168.139.178.sslip.io at adc7a9e in 27 s`.
- `env_file` on exec groups exists: internal/store/configkeys.go:76 (`{"env_file", ConfigString, []string{"exec"}, GroupConfig, ...}`), read in internal/runner/exec.go (`envFile`, line 105), which logs key names and never values (the comment at line 72), and refuses a file that is missing, not regular or open to others, as a herdr group's is (docs/groups.md, "An exec group takes `env_file`").
- `yardr upgrade` refreshes the yard's installed scripts from the checkout it builds; the list is internal/cli/upgrade_scripts.go (line 27 names `contrib/deploy/deploy-dokku`) and docs/upgrade.md, "The scripts".

So this is the second deploy script, not the first deploy stage. What is new is the host (a Void platform, not a Dokku machine) and the vehicle (a pack, which m0n8 5.4 deferred "once the trial has run"; it has run).

### 2.2. The starter pack and how a pack carries a flow

packs/starter/pack.yaml declares two flows: `default` for the depot (backlog, new, review, review-security, approved, decide, merged) and `train` for beads of type train; both end `merged: { end: true }`. Its groups are builders, reviewers, security-reviewers (agents), `assembly` (`run: scripts/merge-local`, limit 1), backlog and decisions (people).

What an import does, read in internal/pack:

- A pack may offer several depot flows; the import asks which, or takes `--flow` (plan.go, `chosen`, lines 814–855). Flows with a `type:` (the starter's `train`) are imported whatever depot flow is chosen (plan.go:849–853).
- Every group the pack declares is planned, whichever flow is chosen: `groups` loops over all of `b.p.Groups` (plan_groups.go:136 onward) with no test of whether a chosen flow names the group.
- A script group (`run:`) becomes an exec group named `<depot>-<group>` (plan.go:595, `GroupName`), its `cmd` the copied script. The pack owns only the script word of `cmd`: assignments before it and arguments after it are the yard's, kept across imports (plan_groups.go:263, `setScript`; docs/packs.md, line 430: "the assignments before the command and the arguments after it (`YARDR_PUSH=origin … merge-local`), `log`, `dir` and the rest"). So an operator's `VOID_PROJECT=… ` in front of the script, and the group's `env_file`, survive `pack apply` run again.
- A pack's files may not leave its directory (pack.go:1446, "leaves the pack's directory"), so one pack cannot borrow another's roles. Packs that are variants of the starter carry copies: packs/dir, packs/contributors and packs/design each have their own `roles/builder.md`, and `packs/starter/scripts/merge-local` is held equal to `contrib/assembly/merge-local` by `TestStarterMergeLocalIsTheReferenceAssembly` (internal/pack/pack_test.go:354).
- `setup:` names a markdown file of setup notes, allowed for a pack of any kind; `pack check` and `pack apply` print it (docs/packs.md, line 163).

### 2.3. Void, as its CLI and its documentation say

Read on 21 Oct 2026 from the npm package `void` 1.1.12 (`npm view void`: "Full-stack framework and deployment CLI for Cloudflare Workers, powered by Vite", bin `void`), installed into a scratch directory and run under `env -i` with a scratch `HOME`. "The CLI" below means its `--help` output or its shipped source (`node_modules/void/dist/…`); "the docs" means the documentation shipped in the same package (`node_modules/void/docs/…`), which is the site's source; the bead's citation, void.cloud/guide/deployment, is docs/guide/deployment.md there.

Where the bead's account and the CLI disagree, the CLI wins:

| Claim (the bead's source) | What the CLI or docs say | Verdict |
|---|---|---|
| A machine-readable output flag for the URL may exist (`--json`) | `void deploy --help` lists `--project`, `--dir`, `--spa`, `--skip-build`, `--platform`, `--require-email`, `--provision`, `--atomic`, `--debug`; no `--json`. Only `void info` and `void project token` take `--json` | No flag. The URL is in the summary box (its title: `note(formatDeploySummary(…), result.url)`, dist/deploy-*.mjs) and, structured, in the deploy's own log (below) |
| A deploy token is minted with `void auth token` (community use) | `void auth` has `login`, `status`, `logout` only (`void help auth`). `void account token` "Copy auth token to clipboard" is a human login. The deploy credential is `void project token create [--project <slug>] [--name <n>] [--expires-in <1–90>] [--json]` (dist/cli/cli.mjs, `parseProjectArgs`; docs/reference/cli/project.md) | `void auth token` does not exist. Use `void project token create` |
| Self-hosted runs `void connect "$VOID_API_URL" --no-login` then `void deploy --platform void --project "$VOID_PROJECT"` with `VOID_TOKEN`, `VOID_API_URL` and the slug set | docs/guide/deployment.md, "Team platform CI", and docs/guide/platform/installation/first-deployment.md give exactly this. The CLI requires it: every command resolves the platform through a saved connection (`connectedPlatform`: "No connection to … Run `void connect …` first", dist/auth-*.mjs) and `VOID_TOKEN` is refused without a matching `VOID_API_URL` ("Set VOID_API_URL alongside VOID_TOKEN …", `readTokenForOrigin`) | Confirmed. `void --help` lists only `VOID_TOKEN` and `VOID_PROJECT` under Environment; `VOID_API_URL` is read all the same |
| After a successful deploy Void prints the URL in a summary box | docs/guide/deployment.md line 9; the CLI's `note(…, result.url)` | Confirmed, as a box drawn for a terminal |
| `void project rollback` rolls back code only, never migrations | docs/reference/cli/project.md, `void project rollback [deployId]`: "Rollback doesn't reverse database migrations"; the CLI warns "Rolling back will NOT revert these migrations. The older code will run against the current schema." and asks to confirm (dist/rollback-*.mjs, `confirmUnrevertedMigrations`) | Confirmed |
| The first deploy links the tree (`.void/project.json`) | The CLI writes `.void/project.json` on a deploy when the tree is not linked (`shouldLinkDeployProject`, `writeProjectConfig`, dist/project-*.mjs). New apps ignore `.void` in `.gitignore` (dist/init-*.mjs, `SCAFFOLD_GITIGNORE_LINES`; first-deployment.md says how to commit the link instead) | Confirmed, with the nuance that the link is local and ignored by default, so `--project` (or `VOID_PROJECT`) is what names the project in a fresh checkout |

What else the CLI and docs establish, which the design depends on:

- **The project token.** "Manage revocable `aud: deploy` credentials for one Void platform project. The credential can only call the endpoints used by `void deploy`; it cannot access operator, account, secret-writing, project-deletion, or other projects' routes." Expiry is 1–90 days; `create` and `renew` print the bearer once, as `VOID_TOKEN=…` and `VOID_API_URL=…` lines; `renew` revokes the previous credential in the same operation (docs/reference/cli/project.md). Creating one requires the project's owner (`createProjectToken`, `permission: "owner"`, dist/client-*.mjs) signed in as a human ("Sign in with a human account before managing CI credentials", dist/project-token-*.mjs). "A human login token is not a CI credential" (first-deployment.md).
- **A saved login is no service credential.** The CLI keeps logins in the system keychain and refuses to store one in plain text ("Refusing to store your platform token in plaintext … set VOID_TOKEN from a secret manager in headless environments", `savePlatformToken`). That answers note 7920's open point (service token or saved CLI login): the project token.
- **Project creation is not the token's.** With `--project <slug>` and no terminal, a project that does not exist is created (`createProjectFromSlug`, dist/deploy-*.mjs), which a deploy-only token cannot do. So the first deploy that creates the project is a person's, signed in.
- **The deploy log.** Every `void deploy` writes a JSON-lines log to `$HOME/.void/logs/deploy-<time>-<pid>-<hex>.jsonl` (`openDeployLog`). Its last record on success is `{"lvl":"info","msg":"deploy_success","deploymentId":…,"url":…,…}`, or `deploy_degraded` with the same fields when "Routing was not published for this deploy. The project may be unreachable (first deploy) or still serving the previous deploy"; a degraded deploy still exits 0 ("this is a warning, not a failure, so the process exit code is unaffected", `warnUnpublishedRouting`). The log's format is the CLI's own, not a documented interface.
- **The CLI version.** "Keep the CLI and framework adapters on matching versions, and commit `void.config.ts` and `void.lock.json` when generated" (deployment.md). A Void app has `void` in its own `package.json`, so the CLI that deploys it is the one its lockfile pins.
- **Rollback.** `void project rollback [deployId]` takes no `--project`: it needs a linked directory ("No linked project. Run `void project link` first.") and, without an id, asks on a terminal which retained deployment to restore. `void project status` lists deployments. Since the project token may call only the endpoints `void deploy` uses, a rollback is made with a person's login. On a self-hosted platform deploy saves no database restore point: that is a direct-Cloudflare feature ("On your own Cloudflare account, deploy saves a D1 Time Travel restore point …", deployment.md line 38), so a migration on a platform is undone by a new migration.
- **What a platform costs its owner.** docs/guide/platform/installation/prerequisites.md: a Cloudflare account with Workers for Platforms (a paid plan), R2 and Analytics Engine; an empty hosted PostgreSQL database; a login provider (GitHub, Google, OIDC or Cloudflare Access); a domain for apps, or `workers.dev` for testing. Install with `void platform install` (Node 24.21 or later). docs/guide/self-hosted-platform.md: "If you're deploying an app for yourself, deploy directly to Cloudflare. You don't need to install a platform first."
- **Where the Cloudflare tokens live.** credentials.md: a management token, kept "in this installation's encrypted local checkpoint" on the machine that ran the installer and used for upgrades and repairs; and a runtime token "stored as a Worker secret so the platform can deploy apps after you close your terminal". Neither is needed to deploy an app.
- **The address.** "Apps will be served at `my-app.example.app`" under the platform's application domain (prerequisites.md). "Custom application domains: Unavailable in the core installation" (self-hosted-platform.md, the feature table).

## 3. The proposal

The smallest thing: a person imports a `website` pack into the depot of their site, gives the yard their platform's project token once, and from then on every landing goes live on their platform, with the address on the bead. It is carried by what exists (a pack, an exec group with `env_file`, a deploy script in the shape of `deploy-dokku`); what is new is one script and one pack.

### 3.1. The vehicle: a `website` pack beside the starter

`packs/website/` is a pack of kind `git`, embedded like the others (`packs/packs.go`'s `//go:embed` line). It is the starter made for one kind of depot, as packs/dir is: the starter's `default` flow with deploy after landing, its groups plus one, its roles, stage files and `merge-local` as copies held equal by tests, and a README and `setup:` notes for the person whose website it is.

```yaml
name: website
kind: git
setup: SETUP.md
flows:
  default:
    stages:
      backlog:  { group: backlog,   file: flows/default/backlog.md,  manual: [new, decide] }
      new:      { group: builders,  file: flows/default/new.md,      outcomes: { done: review, question: decide }, manual: [backlog] }
      review:   { group: reviewers, file: flows/default/review.md,   outcomes: { done: review-security, done-safe: approved, changes: new, question: decide } }
      review-security: { group: security-reviewers, file: flows/default/review-security.md, outcomes: { done: approved, changes: new, question: decide } }
      approved: { group: assembly,  file: flows/default/approved.md, outcomes: { done: merged, failed: new, question: decide } }
      merged:   { group: deploy,    file: flows/default/merged.md,   outcomes: { done: live, failed: decide } }
      decide:   { group: decisions, file: flows/default/decide.md,   manual: [backlog, new, merged] }
      live:     { end: true }
groups:
  builders:  { agent: roles/builder.md,  limit: 5 }
  reviewers: { agent: roles/reviewer.md, limit: 3 }
  security-reviewers: { agent: roles/review-security.md, limit: 2, binds_as: reviewers }
  assembly:  { run: scripts/merge-local, limit: 1 }
  deploy:    { run: scripts/deploy-void, limit: 1 }
  backlog:   { people: true }
  decisions: { people: true }
```

The edges are m0n8 section 3.1's, as signalbox runs them: `merged -> live on done | decide on failed`, and `decide` may send a bead back to `merged` once a person has fixed what failed. `flows/default/merged.md` is the stage file a person sees for a bead there: the site is being deployed; a failure means the code has landed and only the platform or the token can be fixed.

The pack carries no train flow. A website worked by one person rarely needs trains; a train bead in this depot then follows the depot's flow (a type with no flow follows the depot's, docs/flows.md) and so deploys when it lands, which the starter's `train` flow, ending at `merged`, would not. The crew member and the `sources:` lines of the starter come over as they are, with `live: done` beside `merged: done` in Linear's `hears`, so an issue is done when the site is live.

Why a pack of its own rather than a third flow in the starter is open question 5.1. In short: the starter's planner makes every group the pack declares whichever flow is chosen (section 2.2), so every starter depot would get an idle `<depot>-deploy` group; and the starter's `train` flow, imported for every depot, ends at `merged`, so trains in a website depot would not deploy. A pack of its own has neither problem, at the cost of copies a test keeps equal. This differs from m0n8 5.4, which took hand wiring first: that was right before any deploy had run, and the trial since has settled the shape, so the flow is now worth carrying whole.

### 3.2. The deploy stage: `deploy-void`

`contrib/deploy/deploy-void` is the reference script, beside `deploy-dokku`, and `packs/website/scripts/deploy-void` is a copy held equal to it by a test, as merge-local is. It keeps `deploy-dokku`'s contract wherever Void does not force a difference: the environment it reads (`YARDR_BEAD`, `YARDR_DEPOT`, `YARDR_DEPOT_PATH`, `YARDR_BASE`, `YARDR_TRAIN`, `YARDR_BIN`, `YARDR_HOME`), options as assignments in the group's `cmd`, coalescing on the base, the train gate, the outcomes and their notes, mail to `YARDR_NOTIFY` (default the yard's yardmaster) on failure, a hold on a signal.

Options:

| option | where it is set | |
|---|---|---|
| `VOID_PROJECT` | the group's `cmd` | the project's slug on the platform |
| `VOID_TOKEN` | the group's `env_file` | the project deploy token (section 3.3) |
| `VOID_API_URL` | the group's `env_file` | the platform's API URL, printed with the token by `void project token create`, which the CLI requires beside it |
| `VOID_INSTALL` | the group's `cmd` | the command that installs the tree's dependencies; default `npm ci` |
| `YARDR_NOTIFY` | the group's `cmd` | as for `deploy-dokku` |

What a run does:

1. **Refuse before anything.** No `VOID_PROJECT`, `VOID_TOKEN` or `VOID_API_URL`; a `VOID_PROJECT` that is not a slug; `VOID_API_URL` not `https://`. Each refusal reports `failed` with the reason.
2. **The train gate.** A child of a train (`YARDR_TRAIN` set and `YARDR_BASE` that train's branch) deploys nothing and advances `done` with a note naming the train, as `deploy-dokku` does.
3. **Check out what is deployed.** `sha=$(git -C "$YARDR_DEPOT_PATH" rev-parse "refs/heads/$YARDR_BASE")`, then `git -C "$YARDR_DEPOT_PATH" worktree add --detach "$tmp/tree" "$sha"` in a directory of its own, removed with `git worktree remove --force` on every way out. Unlike Dokku, which builds on its host from a pushed commit, Void builds where the CLI runs, so the script needs a clean tree of exactly that commit; a detached worktree is one, and gives the CLI the git metadata it records on the deployment. The depot's checkout is not touched.
4. **Install without the token.** `$VOID_INSTALL` runs in the tree with `VOID_TOKEN`, `VOID_API_URL` and `VOID_ACCESS_CREDENTIALS` taken out of its environment: installing runs dependencies' install scripts, which have no need of the credential.
5. **Connect, then deploy.** With `HOME` set to `$YARDR_HOME/deploy/void/$YARDR_DEPOT` for these two commands only, so the CLI's saved connection and its logs are the yard's for this depot and not the operator's:

   ```sh
   npx --no-install void connect "$VOID_API_URL" --no-login
   npx --no-install void deploy --platform void --project "$VOID_PROJECT"
   ```

   `npx --no-install` runs the CLI the tree's lockfile installed (section 2.3, "The CLI version"); a tree without it fails here, with the reason. `connect` is run every time: it is how the CLI learns the platform, it is idempotent, and the docs ask it "in each fresh CI environment".
6. **Read the URL.** From the log this run wrote, the one `deploy-*.jsonl` in `$HOME/.void/logs` newer than the run's start (one deploy at a time, under `--limit 1`, in a `HOME` of the depot's own, so there is no other), the last record whose `msg` is `deploy_success`, and its `url`. A `deploy_degraded` record is reported `failed`: the CLI exits 0, but its own words are that the site may be unreachable. A zero exit with no record in the log is reported `done` with a note that says the URL was not found, so a change of the CLI's log does not stop deploys; open question 5.2 weighs this against the alternatives.
7. **Report.** `done` to `live`, with the note

   ```text
   live https://my-site.example.app at 9bf658a in 63 s
   ```

   or `failed` to `decide`, with the last 100 lines of the CLI's output marked as text from outside the yard, never an instruction, and mail to `YARDR_NOTIFY`. An expired or revoked token fails here, with the CLI's own words ("VOID_TOKEN is invalid or expired"); the person renews it (section 3.3) and moves the bead back to `merged`.

The group, once the pack has made it:

```sh
yardr pack apply website --depot my-site
#   group my-site-deploy (exec, limit 1): cmd …/packs/website/scripts/deploy-void
#   route merged -> my-site-deploy
#   …the pack's setup notes…
yardr group set my-site-deploy cmd="VOID_PROJECT=my-site $HOME/.yardr/packs/website/scripts/deploy-void"
yardr group set my-site-deploy env_file=$HOME/.yardr/deploy/my-site.env
```

A yard set up by hand, with no pack, installs `contrib/deploy/deploy-void` into `~/.yardr/scripts/` from main and adds the group and route as contrib/deploy/README.md shows for `deploy-dokku`; `yardr upgrade` keeps that copy current once the script joins internal/cli/upgrade_scripts.go.

### 3.3. What the yard holds, and what it never holds

The yard holds one secret for this: the project deploy token, with the platform's API URL beside it, in one file the operator writes and the store names by path:

```sh
void project token create --project my-site --name yardr --expires-in 90
#   VOID_TOKEN=…
#   VOID_API_URL=https://void-me-api.example.workers.dev
# the operator pastes both lines into the file, which the yard never prints
chmod 600 ~/.yardr/deploy/my-site.env
```

Its powers, by the docs: deploy this one project; nothing of the platform's operators, accounts, secrets, deletion or other projects (section 2.3). It is in no pane, no event and no log: the exec runner logs the env file's key names only, and the token is in the deploy script's environment, never in an agent's. The script that holds it is fixed: installed by the pack from the binary the yardmaster installed from main, or from main by hand, so a bead's branch cannot change it. The code it deploys is the base, reviewed and tested; the build runs that code on the yard's machine with the token in the CLI's environment, and dependency installation does not see it (section 3.2, step 4).

The yard never holds a Cloudflare token. Deploying to a platform needs none: the platform deploys with its own runtime token, a Worker secret in the owner's Cloudflare account, and the management token stays in the installer's encrypted checkpoint on whichever machine ran `void platform install` (section 2.3). If that machine is the yard's, the token is on the machine but in nothing of the yard's; SETUP.md tells the person to run the installer where they keep their password manager, and says why. The yard holds no human Void login either: `void project token create` and `void project rollback` are run by the person, in their own terminal, signed in as themselves.

Rotation: the token expires in at most 90 days. The owner runs `void project token renew <id> --expires-in 90`, which revokes the old one in the same operation, and replaces the two lines in the file. Until they do, deploys fail to `decide` with the CLI's words; nothing else in the yard breaks.

The security review sees, against its checklist: a new caller (the yard to the user's platform API, over HTTPS, from the deploy script only); secrets (one project-scoped token in a 0600 env file, nothing in the store, the log or a pane; no Cloudflare credential); text from outside the yard (the CLI's output and the deploy log, quoted in notes as such); listeners (none new in the yard); dependencies (Node and the depot's own `void` package, pinned by its lockfile, already what the depot builds with).

### 3.4. The way back

A bad landing is undone in two steps, and the design names both so neither is taken for the other:

1. **Stop the harm now: `void project rollback <deployId>`, by a person.** Run by whoever is signed in to the platform as the project's owner or a collaborator who may deploy, from any directory linked to the project (`void project link my-site` once, in a checkout of the site), with `void project status` to pick the deployment. The yard does not run it: the project token can call only the endpoints `void deploy` uses, and a rollback is a choice about the live site that the person makes. Rollback restores code only. **It does not reverse database migrations: the older code runs against the newer schema**, the CLI says so and asks to confirm, and on a self-hosted platform there is no restore point to undo a migration with (section 2.3); a migration is undone by a new migration.
2. **Make it stick: revert on the base.** Deploys coalesce on the base, so the next landing deploys the base again and undoes the rollback. The person files a bead to revert the change (or tells the yardmaster to); it lands, and its deploy makes the base and the live site agree. Until then the person may pause the depot's deploy group (`yardr group set my-site-deploy --limit 0`, set back to 1 to resume) so no landing goes live over the rollback; landed beads wait at `merged` meanwhile.

The bead whose landing was rolled back stays closed at `live`: the person adds a note to it saying what they rolled back to and why, since the yard does not see a rollback. SETUP.md and the pack's README carry these steps in this order, with the migrations warning in bold.

### 3.5. What the person sets up

The pack's SETUP.md, as a checklist, in the order it is done:

1. **Cloudflare.** An account with Workers for Platforms enabled (a paid plan), R2 and Analytics Engine; a domain added as a zone, or `workers.dev` to try it first (prerequisites.md).
2. **A database and a login provider.** An empty hosted PostgreSQL database; a GitHub, Google or OIDC application for signing in (prerequisites.md, setup.md).
3. **The Void platform.** `void platform install --plan`, then `void platform install`, on the person's own machine, pasting the Cloudflare management and runtime tokens into its masked prompts. The yard is not involved, and neither token is given to it. Then `void platform status` and `void platform auth login` (first-deployment.md).
4. **The site.** A git repository made by `void init` (or an existing Void app), with `void` in its `package.json` and its lockfile committed; added to the yard as a depot (`yardr depot add`), and the `website` pack applied to it.
5. **The first, linking deploy, by the person.** In a checkout of the site: `void connect <api url>` (signing in), then `void deploy --platform void --project my-site`, confirming project creation. This creates the project, which the token cannot. The site is live once, at the address the summary box names.
6. **The token.** `void project token create --project my-site --name yardr --expires-in 90`; the two printed lines into a 0600 env file; `yardr group set my-site-deploy env_file=…` and `VOID_PROJECT=my-site` in its `cmd`. A note in the person's calendar to renew it.
7. **Prove it.** Land one small change; the bead's note reads `live https://… at <sha> in <n> s`; open the address.

The address is `<project>.<application domain>`: a core platform has no custom domains (section 2.3). A person who wants their site at their own domain's root is told so in SETUP.md's first lines, before they buy anything (open question 5.4).

### 3.6. The first trial

A new depot made for it: an empty repository `void init`ed as a small static site, added to this yard, the `website` pack applied, deployed to a Void platform mckean installs in his own Cloudflare account on `workers.dev` first, then one landing goes live. Open question 5.3 weighs the alternatives.

## 4. Left out

- **Previews of unlanded beads.** m0n8 5.3 took landings only, and `deploy-dokku preview` exists for Dokku; a Void preview would be a second project per bead with its own token, which the token model (one project each) makes costly. A bead after the trial, if a person asks.
- **Rollback by the yard.** The project token cannot, by design of the platform (section 2.3), and holding a human login in the yard is what the CLI itself refuses headless (section 2.3, "A saved login is no service credential"). The person runs it (section 3.4).
- **Deploying straight to Cloudflare (`--platform cloudflare`).** The docs suggest it for one person's app, and it would give a custom domain; but it deploys with the person's Cloudflare credential, which the yard would then hold, against note 7920. Open question 5.4 asks whether that stays ruled out.
- **Cloudflare Access in front of the platform API.** The token then needs `VOID_ACCESS_CREDENTIALS` too (first-deployment.md). The env file can carry it and the script passes the environment through unchanged, but nothing here tests it; SETUP.md says the core install is without Access.
- **Token expiry warnings.** The yard could read `void project token list --json` and warn before expiry, but that needs the owner's login, not the token. The person's calendar, and a deploy failing to `decide` with the CLI's words, carry it.
- **Offering Void in the paid connect service**, **Dokku and signalbox**, and **migrating yardr.dev**: out of scope, as the bead says (mckean's business decision; m0n8's host and depot, untouched; yardr.dev deploys already, outside the yard).
- **A train flow for the pack.** Section 3.1: trains follow the depot's flow and deploy as they land. A person who wants the starter's GitHub train lane imports it separately.

## 5. Open questions

### 5.1. A `website` pack of its own, or the starter with a third flow?

- **A `website` pack beside the starter (section 3.1).** Costs copies of the starter's roles, stage files and merge-local, held equal by tests (the precedent of packs/dir and packs/design, and `TestStarterMergeLocalIsTheReferenceAssembly`); a second pack to name in the docs. A website depot gets exactly its flow and groups, and trains deploy.
- **The starter gains a `website` flow and a `deploy` group.** Costs every starter import an idle `<depot>-deploy` exec group, unless the planner learns to make only the groups the chosen flows name (a core change, general but new); and trains in a website depot end at `merged` without deploying, since the starter's `train` flow is imported for every depot, until the next ordinary landing carries their work.

Take the `website` pack.

### 5.2. Where does the script read the URL?

- **The deploy's own JSON-lines log, `deploy_success.url` (section 3.2, step 6).** Costs a dependence on a log format the CLI does not document, pinned per depot by the lockfile; a change of it costs a note without the URL, not a failed deploy. It is the exact URL the platform returned, and it tells a degraded deploy from a good one.
- **The summary box on the CLI's output.** Costs parsing a box drawn for a terminal (borders, wrapping, colour when a terminal is detected); it is the one the docs name, but it carries no degraded marker of its own.
- **A `VOID_URL` the operator sets, as `DOKKU_URL` is.** Costs nothing to parse, and is always there; but it is the operator's guess, not what the platform said, and a deploy to the wrong project would still read right.

Take the log.

### 5.3. Which site is the first trial?

- **A new site depot, `void init`ed for the trial, on a platform mckean installs on `workers.dev`.** Costs a new repository and the platform install (the paid Workers for Platforms plan, a PostgreSQL database, a login provider); it exercises the pack end to end, from `pack apply` to `live`, which is what this design adds.
- **signalbox.** Costs moving a depot that already deploys to Dokku: its flow is governed by its repository, not a pack, so the trial would test the script and not the pack, and its live address would change.
- **None until a user asks.** Costs nothing now; the pack and script land tested against stand-ins only, and the first real deploy is a user's.

Take a new site depot.

### 5.4. Is the platform's address enough for a website?

- **Yes: the site lives at `<project>.<application domain>`.** Costs a person who wants `www.their-name.com` an application domain of their own (a spare domain whose subdomains are their apps) rather than their exact name; the core platform offers no more (section 2.3).
- **Allow `--platform cloudflare` as a second mode of the script, for custom domains.** Costs the yard holding the person's Cloudflare credential, which note 7920 rules out, and a second auth path to test.
- **Wait for a platform built with custom project domains.** Costs a source-built platform (credentials.md: the runtime token then needs SSL and Certificates: Edit) and nothing this design can verify today.

Take the platform's address, said plainly in SETUP.md's first lines.

## 6. The beads it would become

In the order they would land. 6.1 and 6.2 land in yardr; 6.3 lands in yardr and makes the trial's own depot. Each tests what starts agents or servers in the isolated yard (`contrib/isolated-yard/isolated-yard`), never the operator's, and drives `void` as a stand-in (internal/scripttest); only 6.3 touches a real platform. 6.2 waits on 6.1; 6.3 waits on both and on mckean's platform. If 5.1 takes the starter instead, 6.2 becomes "the starter gains a website flow" with the same done-when, plus the planner change it names. If 5.2 takes another source of the URL, it changes 6.1's step 6 only.

### 6.1. `contrib/deploy/deploy-void`: deploy the base to a Void platform from `merged` (yardr-0a8m)

Goal: the reference script exists, tested, beside `deploy-dokku`, kept current by `yardr upgrade`. (depot yardr)

Where to look: this design, section 3.2; `contrib/deploy/deploy-dokku`, its README and `contrib/deploy/deploy_dokku_test.go` (the pattern: environment, options as assignments, train gate, outcomes with notes, mail, the stand-in for the remote side); internal/runner/exec.go (the environment and `env_file`); internal/cli/upgrade_scripts.go and docs/upgrade.md, "The scripts"; the Void docs and CLI as section 2.3 cites them (npm package `void`, its `docs/guide/deployment.md` and `docs/reference/cli/project.md`).

Done when: `contrib/deploy/deploy-void` refuses a missing or malformed `VOID_PROJECT`, `VOID_TOKEN` or `VOID_API_URL` before running anything; deploys nothing for a child of a train and advances `done` with a note naming it; checks out `YARDR_BASE`'s commit as resolved at the run in a detached worktree of its own and removes it on every way out, the depot's checkout untouched; runs `VOID_INSTALL` (default `npm ci`) without `VOID_TOKEN`, `VOID_API_URL` or `VOID_ACCESS_CREDENTIALS` in its environment; runs `npx --no-install void connect "$VOID_API_URL" --no-login` and `npx --no-install void deploy --platform void --project "$VOID_PROJECT"` with `HOME` at `$YARDR_HOME/deploy/void/$YARDR_DEPOT`; reads the URL from the run's own deploy log; advances `done` to `live` with `live <url> at <sha> in <n> s`, or with a note that the URL was not found when the log has none; reports `failed` with the CLI's last 100 lines marked as outside text and mails `YARDR_NOTIFY` on a non-zero exit or a `deploy_degraded` record; holds on a signal. Tests run it through the exec runner against a local repository with a stand-in `npx`/`void` that writes a deploy log as the CLI 1.1.12 does, and prove each of these, that the token never reaches the install command, that two queued landings coalesce, and that the stand-in saw exactly the two commands above; contrib/deploy/README.md gains its section; internal/cli/upgrade_scripts.go and docs/upgrade.md name it; `sh .yardr/test` passes.

### 6.2. `packs/website`: the starter's line with a deploy stage, for a website on a Void platform (yardr-2929)

Goal: `yardr pack apply website --depot <site>` gives a depot the flow of section 3.1 and a `<site>-deploy` group that runs `deploy-void`. (depot yardr)

Where to look: this design, sections 3.1, 3.3–3.5; packs/starter (pack.yaml, roles, flows/default, scripts, README.md); packs/dir and packs/design (variant packs with copied files); packs/packs.go (the `//go:embed` line) and packs/packs_test.go (the list of embedded packs, the roles checks); internal/pack/pack_test.go (`TestStarterMergeLocalIsTheReferenceAssembly`); docs/packs.md (`setup:`, script groups and what the import keeps); bead 6.1.

Done when: `packs/website/` holds pack.yaml as section 3.1 shows (one depot flow, no train flow, `merged` routed to `deploy`, `live` the end, `decide` able to send a bead back to `merged`, Linear's `hears` with `live: done`), the starter's roles, stage files and merge-local as copies, `flows/default/merged.md`, `scripts/deploy-void`, a README, and SETUP.md carrying section 3.5's checklist, section 3.4's way back with the migrations warning in bold, section 3.3's rotation, and the address limit of 5.4 in its first lines; tests hold each copy equal to the starter's file or to `contrib/deploy/deploy-void` and the scripts executable; the pack is embedded and listed; in the isolated yard, `pack check website --depot <d>` and `pack apply website --depot <d>` succeed, make `<d>-deploy` an exec group at limit 1 whose `cmd` is the copied script, route `merged` to it, and a second `pack apply` after `yardr group set <d>-deploy cmd="VOID_PROJECT=x …"` and `env_file=…` keeps both; docs/packs.md names the pack where it lists the embedded ones; `sh .yardr/test` passes.

### 6.3. Trial: one landing of a new site goes live on mckean's Void platform (yardr-q4bf)

Goal: the design's first sentence is true for one real site. (depot yardr; the bead makes the site's own depot)

Where to look: beads 6.1 and 6.2; packs/website/SETUP.md; the answers to 5.3 and 5.4; signalbox-24sp (the Dokku trial: what was run, where, and how it was undone).

Done when: mckean has installed a Void platform in his Cloudflare account (steps 1–3 of section 3.5; nothing is installed before he says so, and no Cloudflare token reaches the yard); a new repository made by `void init` is added as a depot and the `website` pack applied to it; mckean has made the first, linking deploy and created the project token, and the env file is in place at mode 0600 (the yard records its path, never its content); one trivial change lands and its bead's note carries the platform's URL, opened in a browser; `void project rollback` is tried once by mckean on the site, then the next landing shown to deploy the base again; the bead's note says what was run, where, and how to undo it (the depot, group and route removed, the token revoked, the project deleted).
