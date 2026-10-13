# contrib/deploy

One shell script, run as the `cmd` of an `exec` group with `--limit 1` (one
deploy at a time), routed for the stage a landed bead enters. The design is
[deploy as a stage of the flow](../../docs/design/2026-10-13-yardr-m0n8-deploy-stage-dokku.md),
section 3.1.

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
nothing of the tree it pushes is run here. It needs `git` and `ssh` on PATH.
Compare the key `ssh-keyscan` printed with the host's own
(`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`, on the host) before
trusting the file: the scan believes whoever answers.

| option | |
|---|---|
| `DOKKU_HOST` | the host, a name or an address; the push goes to `dokku@$DOKKU_HOST` |
| `DOKKU_APP` | the app on it |
| `DOKKU_KNOWN_HOSTS` | a known-hosts file with the host's ssh key |
| `DOKKU_KEY_FILE` | the private key to push with; ssh's own default keys when unset |
| `DOKKU_URL` | where the app is opened, for the note; the note names the host and the app when unset |
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
known-hosts file, with strict checking, so a host whose key is not in the
file, or no longer the one in it, fails the push, and the note says that it
was the key. Nothing is ever asked on a terminal (`BatchMode`): a key with
a passphrase fails too. The rest of the ssh configuration of the user the
daemon runs as still applies (a port, a jump host). With `DOKKU_KEY_FILE`
that key alone is offered; the yard holds its path, never its material.

Before anything is pushed it refuses: no `DOKKU_HOST` or `DOKKU_APP`, or
one that is no host or app name; no `DOKKU_KNOWN_HOSTS`, or a file that is
missing or cannot be read; a `DOKKU_KEY_FILE` that is missing or cannot be
read; and a path of either with a space, `%`, `$`, a quote or a backslash
in it, which ssh would read as another file.

A refusal and a failed push both report `failed` (to `decide`): the code
has landed, so only the host or this setup can be fixed, and a person sends
the bead back to `merged` when it is. The note carries the last 100 lines
of the push's output, marked as text from the host, and the yardmaster is
mailed. A flow with no `failed` outcome from the stage gets the bead held
instead. A push ended by a signal is held too: it says nothing about the
host.

`go test ./contrib/deploy/` runs it through the exec runner against a local
repository behind a stand-in for ssh: the push, each outcome, the train
gate, the pinned key and every refusal.
