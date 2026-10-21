# contrib/deploy

Two shell scripts, each run as the `cmd` of an `exec` group with `--limit 1`
(one deploy at a time), routed for the stage a landed bead enters.
**deploy-dokku** pushes to a Dokku app, with `preview` as its argument for a
stage a bead enters before it lands
([below](#a-preview-of-a-bead-that-has-not-landed)); **deploy-void** deploys
to the depot's project on the operator's own Void platform
([below](#deploy-void)). The designs are [deploy as a stage of the
flow](../../docs/design/2026-10-13-yardr-m0n8-deploy-stage-dokku.md), section
3.1, and [the website pack with a Void
deploy](../../docs/design/2026-10-21-yardr-c76b-website-pack-void.md),
section 3.2.

**deploy-dokku** pushes a depot's base to a [Dokku](https://dokku.com/docs/)
app after each landing. The depot's flow goes on from `merged` instead of
ending there:

```text
approved -> merged on done | new on failed | decide on question
merged   -> live on done | decide on failed
decide   -> merged
live     terminal
```

```sh
install contrib/deploy/deploy-dokku ~/.yardr/scripts/    # from main
ssh-keyscan -t ed25519 dokku.example.com > ~/.yardr/deploy/known_hosts
yardr group add app-deploy --runner exec --limit 1 \
  --set cmd="DOKKU_HOST=dokku.example.com DOKKU_APP=app DOKKU_KNOWN_HOSTS=$HOME/.yardr/deploy/known_hosts DOKKU_URL=https://app.example.com $HOME/.yardr/scripts/deploy-dokku" \
  --set log="$HOME/.yardr/logs/app-deploy.log"
yardr route add --stage merged --group app-deploy --depot app
```

Install it from `main`, not a checkout, so a branch cannot change how it is
deployed: the script that runs is the one the group's `cmd` names, and
nothing of the tree it pushes is run here. Once it is there, `yardr upgrade`
keeps the copy current from the checkout it builds (docs/upgrade.md, "The
scripts"). It needs `git` and `ssh` on PATH.
Compare the key `ssh-keyscan` printed with the host's own
(`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`, on the host) before
trusting the file: the scan believes whoever answers.

| option | |
|---|---|
| `DOKKU_HOST` | the host, a name or an address; the push goes to `dokku@$DOKKU_HOST` |
| `DOKKU_APP` | the app on it (a deploy's; a preview names its own) |
| `DOKKU_KNOWN_HOSTS` | a known-hosts file with the host's ssh key, by its absolute path |
| `DOKKU_KEY_FILE` | the private key to push with, by its absolute path; ssh's own default keys when unset |
| `DOKKU_URL` | where the app is opened, for the note, with `{app}` for the app's name; the note names the host and the app when unset |
| `DOKKU_AFTER` | a command run in the depot, with the deploy's environment, after a deploy's push; the bead goes live only when it exits 0; unset runs nothing |
| `YARDR_NOTIFY` | crew member mailed when a deploy fails (default: the yard's `yardmaster` setting, else `yardmaster`) |

It resolves `YARDR_BASE` to its commit as it runs and pushes that commit to
the app's `main`, by URL: no remote is added to the depot, no checkout is
touched, and nothing is forced. The bead then reports `done` (to `live`)
with what is running, where, and how long the push took:

```text
live https://app.example.com at 9bf658a in 47 s
```

`YARDR_BASE` is the base's name, not the commit the bead landed, so deploys
coalesce: two landings queued behind the group each push the base as it
stands when they run. The second finds the host already there, pushes
nothing new, and still ends `live` with the same note. The commit a note
names may hold a later landing than the bead's own.

A child of a train (`YARDR_TRAIN` set, and that train's branch as
`YARDR_BASE`) landed on the train's branch, which is unreviewed and
untested as a whole: nothing is pushed, and the bead goes on to `live` with
a note that names the train. The train's own run pushes the base, as long
as the flow its type follows has the same `merged` to `live` edge.

The host's ssh key is pinned. ssh reads `DOKKU_KNOWN_HOSTS` and no other
known-hosts file, with strict checking and host-key updates disabled, so a
host whose key is not in the file, or no longer the one in it, fails the
push, and the note says that it was the key. Nothing is ever asked on a
terminal (`BatchMode`): a key with a passphrase fails too. The rest of the
ssh configuration of the user the daemon runs as still applies (a port, a
jump host). With `DOKKU_KEY_FILE` that key alone is offered; the yard holds
its path, never its material.

Before anything is pushed it refuses: no `DOKKU_HOST` or `DOKKU_APP`, or
one that is no host or app name; no `DOKKU_KNOWN_HOSTS`, or a file that is
missing or cannot be read; a `DOKKU_KEY_FILE` that is missing or cannot be
read; and a path of either that is not absolute, or has a space, `%`, `$`,
a quote or a backslash in it, which ssh would read as another file. ssh
runs in the depot, so a relative path would name a file of the tree being
pushed: the pin and the key are kept outside it.

A refusal and a failed push both report `failed` (to `decide`): the code
has landed, so only the host or this setup can be fixed, and a person sends
the bead back to `merged` when it is. The note carries the last 100 lines
of the push's output, marked as text from the host, and the yardmaster is
mailed. A flow with no `failed` outcome from the stage gets the bead held
instead. A push ended by a signal is held too: it says nothing about the
host.

A deploy whose `DOKKU_AFTER` names a command (say
`DOKKU_AFTER=$YARDR_DEPOT_PATH/scripts/serve-public.sh` in the group's
`cmd`, which the runner expands) runs it after a successful push, in the
depot with the deploy's environment, and goes live only when the step
exits 0. A failed step reports `failed` with the step's last 100 lines in
the note, as a failed push does — the push stands, but the bead is not
live — and a step ended by a signal holds the bead. The step's output is
in the group's log. A preview never runs it, nor does the run of a child
of a train, which pushes nothing.

## A preview of a bead that has not landed

`deploy-dokku preview` pushes the bead's branch (`YARDR_BRANCH`) to an app
of the bead's own, for a person in the next stage to open. The flow has a
stage for the script and one a person holds, before the landing:

```text
review  -> preview on done | new on changes
preview -> look on done | decide on failed
look    -> approved | new
```

```sh
yardr group add app-preview --runner exec --limit 1 \
  --set cmd="DOKKU_HOST=dokku.example.com DOKKU_KNOWN_HOSTS=$HOME/.yardr/deploy/known_hosts DOKKU_URL=http://{app}.dokku.example.com $HOME/.yardr/scripts/deploy-dokku preview" \
  --set log="$HOME/.yardr/logs/app-preview.log"
yardr route add --stage preview --group app-preview --depot app
```

**A preview runs code no reviewer has read, on the host.** Its build runs
there too, with whatever the branch's Dockerfile says. Keep the host
unreachable from outside (a private network, or the yard's own machine's),
and keep on it nothing a branch must not reach: previews share it with
every other app it runs. The script gives a preview app no configuration
and no secrets, and nothing may be set for all apps on the host
(`dokku config:set --global`), which a preview would get as well. A
preview that needs a secret to start is not for this script.

The app is `preview-<bead>`, with each character of the depot's name that
is no letter or digit as a hyphen: bead `yardr.dev-ryta` is app
`preview-yardr-dev-ryta`. (Depots named `a.b` and `a-b` would share names:
give one host to one of them.) `DOKKU_APP` is not read. The script makes
the app when the host has none of that name (`apps:create`, over the same
pinned ssh as the push). A bead that comes back from the person and is
built again is pushed to the same app, and that push is forced, where a
deploy's never is: a bead's branch is rewritten between runs, and the app
holds nothing but the last one. The bead reports `done` with the address:

```text
preview http://preview-app-x7k2.dokku.example.com at 9bf658a of yard/app-x7k2 in 47 s.
```

The address is `DOKKU_URL` with the app's name for `{app}`: a host serves
its apps under its own global domain (`dokku domains:report --global`),
which the script does not ask for. A preview refuses a `DOKKU_URL` without
`{app}`.

Every run of the script, a deploy or a preview, first removes the depot's
previews that are over (`apps:destroy`): those of closed beads, and in a
deploy the one of the bead it runs for, which has landed. Each such bead
gets a note that its preview is removed. So the deploy at `merged` and the
preview should name the same host; a depot with no deploy stage has a
landed bead closed at `merged`, and its preview goes with the next run. A
bead closed without landing is covered the same way, by the depot's next
run. When none will come, a person or the yardmaster runs the removal
alone, with the options in the environment:

```sh
DOKKU_HOST=dokku.example.com DOKKU_KNOWN_HOSTS=$HOME/.yardr/deploy/known_hosts \
  ~/.yardr/scripts/deploy-dokku remove app
```

It moves no bead and exits 1 when the host refused a removal. A refused
removal leaves the app in place, adds a note to its bead, and mails the
yardmaster; its output is marked as text from the host, not instructions.
An app whose bead the yard does not know is left alone. A deploy whose key
may push but not list the host's apps deploys all the same and removes
nothing.

A child of a train is previewed like any bead, where a deploy pushes
nothing for one: its branch is the train so far and its own change, in an
app of its own, and the note names the train. Its preview is removed once
it has landed on the train and closed.

A preview refuses what a deploy refuses, before any command on the host,
and besides: a bead with no local branch, and a bead whose id is not the
depot's name, a hyphen, and lower case letters, digits and hyphens (or
whose app's name would pass 63 characters), since the removal could not
find its app again. A refusal, a host that will not list or make the app,
and a failed push all report `failed` with the host's output, and the
yardmaster is mailed; the app stays for the next run. A flow with no
`done` from the stage gets the bead held with the address in the note.

## deploy-void

**deploy-void** deploys a depot's base to the depot's project on the
operator's own [Void](https://void.cloud) platform after each landing. The
depot's flow goes on from `merged` instead of ending there, as with
deploy-dokku:

```text
approved -> merged on done | new on failed | decide on question
merged   -> live on done | decide on failed
decide   -> merged
live     terminal
```

```sh
install contrib/deploy/deploy-void ~/.yardr/scripts/    # from main
void project token create --project site --name yardr --expires-in 90
#   paste the two printed lines into the env file, 0600:
cat > ~/.yardr/deploy/site.env <<EOF
VOID_TOKEN=...
VOID_API_URL=https://...
EOF
chmod 600 ~/.yardr/deploy/site.env
yardr group add site-deploy --runner exec --limit 1 \
  --set cmd="VOID_PROJECT=site $HOME/.yardr/scripts/deploy-void" \
  --set env_file="$HOME/.yardr/deploy/site.env" \
  --set log="$HOME/.yardr/logs/site-deploy.log"
yardr route add --stage merged --group site-deploy --depot site
```

Install it from `main`, not a checkout, so a branch cannot change how it is
deployed: the script that runs is the one the group's `cmd` names, and the
code it deploys is the base, reviewed and tested. Once it is there, `yardr
upgrade` keeps the copy current from the checkout it builds (docs/upgrade.md,
"The scripts"). It needs `git`, `node` and `npx` on PATH: Node 24.21 or
later on the PATH the yard server starts with, since the deploy runs there,
and the `void` package in the tree's own `package.json`, which the tree's
lockfile pins. A failure's note carries `node --version`, so an old Node is
seen at once.

| option | |
|---|---|
| `VOID_PROJECT` | the project's slug on the platform, in the group's `cmd` |
| `VOID_TOKEN` | the project deploy token, in the group's `env_file` |
| `VOID_API_URL` | the platform's API URL, in the group's `env_file` beside it |
| `VOID_INSTALL` | the command that installs the tree's dependencies, in the group's `cmd`; default `npm ci` |
| `YARDR_NOTIFY` | crew member mailed when a deploy fails (default: the yard's `yardmaster` setting, else `yardmaster`) |

What a run does: resolve `YARDR_BASE` to its commit and check it out in a
detached worktree of its own, removed on every way out, the depot's
checkout untouched; run `VOID_INSTALL` in the tree with the credential
taken out of its environment; connect the CLI the tree pins to the platform
and deploy the project, both with a HOME of the depot's own
(`$YARDR_HOME/deploy/void/<depot>`); read the URL from the run's own deploy
log. The bead then reports `done` (to `live`) with what is running, where,
and how long the run took:

```text
live https://site.example.app at 9bf658a in 63 s
```

The URL is the run's own log's last word: the `deploy-*.jsonl` the run
wrote, its last record with `msg` `deploy_success`, and that record's `url`. A `deploy_degraded` record reports `failed`: the CLI exits 0, but
its own words are that the site may be unreachable. A zero exit with no
record in the log reports `done` with a note that the URL was not found, so
a change of the CLI's log never stops deploys.

`YARDR_BASE` is the base's name, not the commit the bead landed, so deploys
coalesce, as Dokku deploys do: two landings queued behind the group each
deploy the base as it stands when they run. A child of a train deploys
nothing and goes on to `live` with a note that names the train; the train's
own run deploys the base.

The credential is one project deploy token with the platform's API URL
beside it, in the 0600 env file whose path the group names; the yard never
prints it. What bounds the harm if it leaks is the token's scope, not its
hiding place: this one project, at most 90 days, revoked or renewed with
`void project token`. Every process of the yard's OS user can obtain it —
agents, by reading the env file, and the site's own install scripts and
build code, which the deploy runs on the yard's machine, by reading the
file or, on Linux, the CLI's environment — so the install step's scrub is
hygiene, not isolation: it keeps the token out of the install's output and
its accidents, not out of its reach. The yard holds no Cloudflare token and
no human Void login; whoever can reach this token can already get code onto
the base through a landing, which the same script then deploys.

Before anything runs it refuses: no `VOID_PROJECT`, `VOID_TOKEN` or
`VOID_API_URL`; a project that is no slug; a URL that is no `https` URL. A
refusal and a failed run both report `failed` (to `decide`): the code has
landed, so only the site, the platform, the token or this setup can be
fixed, and a person sends the bead back to `merged` when it is. The note
carries the last 100 lines of the CLI's output, marked as text from outside
the yard, and says plainly that the fault may be the site's (its build, its
config, a migration) as well as the platform's or the token's; the
yardmaster is mailed. A flow with no `failed` outcome from the stage gets
the bead held instead. A run ended by a signal is held too: it says nothing
about the platform.

There are no previews: a Void preview would be a second project per bead
with its own token. The way back after a bad landing is the person's: `void
project rollback` from a checkout linked to the project, then a revert on
the base, since the next landing deploys the base again. Rollback never
reverses database migrations; a migration is undone by a new migration.

`go test ./contrib/deploy/` runs it through the exec runner against a local
repository behind a stand-in for ssh: the push, each outcome, the train
gate, the pinned key and every refusal; and for a preview the push, the
second, forced one, the app's name, the removal by each kind of run, and
its refusals. The void run drives deploy-void through the same runner
against a stand-in for npx: the checkout, the install without the
credential, the connect and the deploy with the depot's HOME, the URL from
the run's own log, each outcome, the train gate, coalescing, and every
refusal.
