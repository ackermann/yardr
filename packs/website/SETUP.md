# Setting up a website on your own Void platform

Your site will live at `<project>.<application domain>`, the address your
Void platform gives it. A core platform has no custom domains: if you want
your site at your own domain's root, stop here, before you buy anything.
A spare domain of your own works (`my-site.apps.example.com` for a project
`my-site`); your exact name does not.

One more thing to know before you start: the yard holds your platform's
project deploy token, and every agent the yard runs, and the site's own
build, can obtain it, since they run as the yard's OS user. That is
accepted, and this is the statement of it: the token deploys this one
project, lasts at most 90 days, and is revoked in one command; whoever can
reach it can already get code onto the base through a landing, which the
same script then deploys. The token's scope is what bounds it, not its
hiding place. Where each secret lives is below.

## The checklist, in order

1. **Cloudflare.** An account with Workers for Platforms enabled (a paid
   plan), R2 and Analytics Engine; a domain added as a zone, or
   `workers.dev` to try it first.
2. **A database and a login provider.** An empty hosted PostgreSQL
   database; a GitHub, Google or OIDC application for signing in.
3. **The Void platform.** `void platform install --plan`, then
   `void platform install`, on your own machine — not the yard's — pasting
   the Cloudflare management and runtime tokens into its masked prompts.
   The yard is not involved, and neither token is given to it: the
   management token stays in the installer's encrypted checkpoint on the
   machine that ran it, and on the yard's machine that file would sit where
   the yard's OS user, and so every agent, can read it. Then
   `void platform status` and `void platform auth login`.
4. **Node on the yard's machine.** Node 24.21 or later on the PATH the
   yard server starts with, since the deploy runs there; `node --version`
   in the shell that starts `yardr server` shows it. A failed deploy's note
   carries it too, so an old Node is seen at once.
5. **The site.** A git repository made by `void init` (or an existing Void
   app), with `void` in its `package.json` and its lockfile committed;
   added to the yard as a depot (`yardr depot add`), with this pack applied
   to it (`yardr pack apply website --depot <site>`).
6. **The first, linking deploy, by you.** In a checkout of the site:
   `void connect <api url>`, signing in, then
   `void deploy --platform void --project <site>`, confirming project
   creation. This creates the project, which the token cannot. The site is
   live once, at the address the summary box names.
7. **The token.** `void project token create --project <site> --name
   yardr --expires-in 90`; paste the two printed lines (`VOID_TOKEN=…` and
   `VOID_API_URL=…`) into a file only the yard's user can read, and point
   the deploy group at it:

   ```sh
   chmod 600 ~/.yardr/deploy/<site>.env
   yardr group set <site>-deploy env_file=$HOME/.yardr/deploy/<site>.env
   yardr group set <site>-deploy cmd="VOID_PROJECT=<site> $HOME/.yardr/packs/website/scripts/deploy-void"
   ```

   Who can read that file: every agent the yard runs and the site's own
   build, since they run as the yard's OS user. So the token is one you
   accept being used to deploy this site by anything that can change it:
   it deploys this one project, and nothing of the platform's operators,
   accounts, secrets, deletion or other projects. The install step runs
   without it, and the Void CLI keeps it out of the build's environment;
   both are hygiene, not isolation. The yard holds no Cloudflare token and
   no human Void login: creating the token and rolling back are run by you,
   in your own terminal, signed in as yourself.

   The token expires in at most 90 days. Renew it with
   `void project token renew <id> --expires-in 90`, which revokes the old
   one in the same operation, and replace the two lines in the file; put a
   note in your calendar. Until you do, deploys fail to `decide` with the
   CLI's words, and nothing else in the yard breaks.
8. **Prove it.** Land one small change; the bead's note reads
   `live https://… at <sha> in <n> s`; open the address.

## The way back

A bad landing is undone in two steps, in this order, and neither is the
other:

1. **Stop the harm now: `void project rollback <deployId>`, by you.** Run
   by whoever is signed in to the platform as the project's owner or a
   collaborator who may deploy, from any directory linked to the project
   (`void project link <site>` once, in a checkout of the site), with
   `void project status` to pick the deployment. The yard does not run it:
   the project token can call only what `void deploy` uses, and a rollback
   is a choice about the live site that you make. Rollback puts the earlier
   deployment's code back in front of traffic; it may also put back that
   deployment's schedules, queues, workflows and routes, or keep the
   current ones, so check the site's scheduled jobs and routes are the ones
   you expect afterwards, and **rollback does not reverse database migrations**:
   the older code runs against the newer schema; a migration is undone by
   a new migration.
2. **Make it stick: revert on the base.** Deploys follow the base, so the
   next landing deploys the base again and undoes the rollback. File a bead
   to revert the change (or ask the yardmaster to); it lands, and its deploy
   makes the base and the live site agree. Until then you may pause the
   deploy group (`yardr group set <site>-deploy --limit 0`, set back to 1
   to resume) so no landing goes live over the rollback; landed beads wait
   at `merged` meanwhile.

The bead whose landing was rolled back stays closed at `live`: add a note
to it saying what you rolled back to and why, since the yard does not see
a rollback.

## When a deploy fails

A deploy that fails after landing goes to `decide` with the end of the
Void CLI's output. The fault may be the site's own (its build, its config,
a migration) as well as the platform's or the token's: the code has landed,
so what can be fixed now is the platform, the token, or the site's own
code, not the landing. What repairs it is a fix bead forward — a migration
is undone by a new migration, never by going back — or, for a code-only
fault, your own `void project rollback` first and the revert bead after,
with the deploy group paused until it lands. The `merged` stage file says
the same.
