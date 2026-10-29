# Upgrading yardr

`yardr upgrade` installs the depot's build, verified, or rolls back. It
does the hand step whole: from the checkout of the depot the
`upgrade_from` setting names it runs `go install .`, refreshes the
scripts the yard already runs from its home from the same checkout,
checks every covered yard runs the new build, and otherwise puts the
previous build back, the scripts with it. The assembly calls it after
the merge when the setting names the depot; by hand it is run after
pulling instead of `go install .`. A yard without the setting is
untouched.

```sh
$ yardr upgrade
backup    ~/.yardr/backups/yard.db.before-install
keep      ~/go/bin/yardr.prev (1904410)
install   go install . at 1904417
scripts   merge-local, integrate-train refreshed
health    ok in 24 s
```

One line per step, exit 1 when the install fails, 0 when every covered
yard runs the new build. The `scripts` line is [its own
section](#the-scripts).

## The assembly step

`contrib/assembly/merge-local` — and the identical copies the packs
carry — calls it after the push and before the advance: when
`upgrade_from` names the landed bead's depot, it runs the installed
yardr's `upgrade` and folds the one-line summary into the landing note,
with the scripts the install refreshed by name, and the seconds into
the group's log line.

```sh
merged 6a07178, tested with the base's copy of .yardr/test in 355 s; install ok in 24 s (scripts refreshed: merge-local)
```

The code has landed, so a failed install where the yard still answers
never sends the bead back: the bead still merges, with a note saying
what failed and what was put back, as a failed push does. Where the
installed yardr predates the command, the note says to install the new
yardr once by hand first. When the install leaves the yard dead, no
advance is possible: the bead fails its session toward a hold, with the
`<home>/upgrade.failed` marker and the session log as the record. A
wagon never installs: it lands on its train's branch, not the depot's
base, while the install builds the depot's checkout.

The step activates only once the operator has installed the script and
sets `upgrade_from`. The assembly runs the installed copy, so landing
the step changes nothing by itself. In order, the four activations are:
name a notify source (setting `upgrade_from` is refused without one),
install a build that has the command (one `go install .` by hand from
`main`), install the script from `main` (the first-time step of
[line.md](line.md#setup); [the upgrade keeps it current](#the-scripts)
from then on), then set `upgrade_from` to the depot.

A yard whose operator names no notify source cannot set `upgrade_from`
— the setting stays refused, so the automatic path can never install
untold — and takes its upgrades by hand with `yardr upgrade --from
<depot>`, which names the depot for one run instead of the setting. The
caller takes the failure through the exit status and the terminal, as a
hand run does. Like the setting, the flag is the operator's alone: anyone
else is refused. The assembly never passes the flag, so it cannot leak
into the automatic path. Setting `upgrade_from` to a depot the yard has
not got is refused too — add the depot first — and removing the depot it
names is refused until the setting is cleared, so no one can create a
depot under a dangling name and choose the install source.

## Signing the binary

On macOS, a yard that names a code-signing identity signs the installed
binary with it at every upgrade: with `codesign_identity` set, `yardr
upgrade` signs the fresh install with that identity under the identifier
`dev.yardr.yardr`, after the install and before anything gates on the new
build. The run prints a `sign` line of its own, between `keep` and the
`install` line that names the new build. With the setting unset, or off
macOS, nothing is signed.

Without it the install is ad-hoc bytes, and a Developer Tools entry made
for yardr pins that build's cdhash: the next install replaces the binary
and the entry goes stale, so macOS XProtect-scans every test binary the
yard runs until the entry is remade. With the identity set, the entry's
designated requirement — the identifier plus the certificate — survives
the next install, and one entry lasts.

The setup is once, in order:

1. Make a code-signing certificate in the login keychain.
2. `yardr settings set codesign_identity "<the certificate's name>"`.
3. Run `yardr upgrade` once, so the installed binary carries the
   signature.
4. Add the installed yardr — the upgrade target, e.g.
   `~/go/bin/yardr` — in System Settings > Developer Tools.

macOS allows that last step only in the GUI. Add the signed binary, not
an ad-hoc one: an ad-hoc yardr added there goes stale at the next
install, and the scans come back. A hand `go install .` outside `yardr
upgrade` leaves the binary unsigned until the next upgrade; to sign it by
hand, run the line the upgrade runs:

```sh
codesign --force --sign "<the certificate's name>" --identifier dev.yardr.yardr ~/go/bin/yardr
```

## The scripts

The groups of a yard run scripts from `<home>/scripts`: `merge-local` for
the assembly, `integrate-train`, `pr-train` and `pr-merge` for trains,
`merge-pr` and `pr-ci` for the pull requests of outside, `deploy-dokku` and
`deploy-void` for deploy stages, `return-branch` for a peer. The
operator installs each once by hand (`install contrib/assembly/merge-local
~/.yardr/scripts/`, [line.md](line.md#setup)), and a copy is only as new
as that step. A fix that lands in a script then reaches no landing until
someone installs it again. So after `go install .` the upgrade refreshes
the copies from the same checkout, and a fix to a script is live for the
next landing, as a fix to the binary is. It reads the nine scripts from
where the docs have the operator install them from, `contrib/assembly/`
(`merge-local`, `integrate-train`, `pr-train`, `pr-merge`, `merge-pr`, `pr-ci`),
`contrib/deploy/` (`deploy-dokku`, `deploy-void`) and
`contrib/peer/return-branch`, and
writes each under its own name.

It touches only what is there. A script the yard does not have is not
added, since a yard that never set the group up has no use for it, and a
script the checkout does not have leaves the yard's alone. A copy whose
bytes equal the checkout's is not touched and gets no `.previous`.

A copy that differs is replaced, never written in place. `sh` reads a
script as it runs, so writing over one changes what a landing that is
running reads next. The upgrade writes the new file beside it as
`.<name>.new` with mode 0755, checks it with `sh -n`, and renames it over
the old name. The landing that is running keeps the file it opened, and
the next one starts on the new. The bytes replaced stay as
`<name>.previous` in the same directory, one generation, overwritten each
time. That is the way back for a copy someone edited by hand. The upgrade
does not look for hand edits; whatever differs is replaced.

A new file that fails `sh -n` is not installed, and neither is one that
cannot be written. The yard's copy stays as it was, and the upgrade prints
a warning that names it, with the shell's words for the error. This never
fails the upgrade. The binary is installed and verified whatever happens
here, and the warning lands in the log the assembly keeps.

Each covered yard's own `<home>/scripts` is refreshed, the same set the
binary covers, with the scripts each has. The copies a pack carries under
`<home>/packs/*` and `<home>/flows/*` are not refreshed. A pack import owns
those, and no group of a yard set up by hand runs them: they are refreshed
by `yardr pack apply`, and `yardr doctor` names the imports whose copies
are behind what the running binary would copy.

The upgrade says it in one line between `install` and `health`:
`scripts   merge-local, integrate-train refreshed`. The line reads
`scripts   current` when every copy it found was the checkout's, and
`scripts   kept as they were, see the warnings above` when every copy it
tried to refresh was kept, with a warning for each. On a rollback each script this run
replaced goes back from its `.previous`, once the previous binary is
back, because a new script may call what only the new binary knows. The
output then adds `scripts   merge-local put back`, and a put-back copy
has no `.previous` left.

The installed yardr has to be one that does this. The upgrade that
installs the first such build is still run by the old one and refreshes
nothing; the next upgrade does.

## What it covers

The covered yards are this yard, always, plus every yard registered on
this machine whose login service runs the shared binary: the homes the
daemon services name in `WorkingDirectory` whose program is the install
target. A registered yard on another binary is left out — the install
cannot move it — and a yard with no login service is covered only when
it is the upgrading yard. Every yard that runs from the installed path
replaces when any one of them installs, whether its own install step is
on or not; the upgrade answers by backing up, checking and on failure
restoring each, rather than by containing the effect.

Concurrent upgrades serialize beside the binary: `yardr upgrade` holds
an exclusive lock on `yardr.upgrade.lock`, next to the installed binary
and `yardr.prev`, for the whole run, and a second upgrade refuses at
once with a line naming the holder's pid. A run refused on the lock
reports it as an install failure; the next landing tries again.

Before the install it takes a consistent pre-install copy of every
covered yard's store, each to its own
`<home>/backups/yard.db.before-install`, copies the running executable
aside to `yardr.prev` beside it, one generation, and writes
`<home>/upgrade.gate` in every covered yard's home. While that file
names a live process the daemon's passes claim nothing and start no
agent, so no pass on the new build ever starts a session; the file is
removed once the gate passes or the rollback completes. The backup is
unconditional: `upgrade_restore` governs restoring, never copying.

It refuses when `go` is not on its PATH, when the depot has no checkout
on this machine, and when `go install` would not write the binary this
yard runs from: the install's output path is computed as `go install`
does (GOBIN, else the first GOPATH's `bin`) and compared with the
running binary's path, refusing when they differ. Where a depot sits in
a folder macOS protects, it warns that the new build may need its
access granted again, without failing.

## The gate

The gate polls `upgrade --check` through the installed binary as a
subprocess per yard, passing the install's start as `--since`, every 5
seconds up to `--timeout` (default 5 minutes). The yards are checked in
parallel under one deadline for the whole run, so the rewind window
does not grow with the number of yards. Each sample runs the installed
binary, so the predicate always runs in the build it asserts, and each
sample also proves the new binary starts as a CLI. A stopped server is
started first, per yard, so there is always a server to verify, and
left running what was started; nothing else is started. Before the poll
each yard's web view where it runs as a login service is restarted on
the new build (`launchctl kickstart -k` on macOS, `systemctl --user
restart` on Linux), waiting for `/health`, so the view serves what the
install wrote. A yard the gate cannot check fails it and is named in
the marker and the message.

`yardr upgrade --check` is the gate by hand: it asserts the yard runs
this build — the build of the `yardr` that was asked — read-only.

```sh
$ yardr upgrade --check
server    ok: answers on 1904417, this build
daemon    ok: holds the lock on 1904417, this build, pass 12 s ago
views     ok: 1 answers on 1904417; 1 old: restart it (yardr web stop, then yardr web start) (http://127.0.0.1:8792/)
```

One line per check, exit 1 when any part fails, 0 otherwise. The check
is read-only: a server that does not answer makes it fail with why; it
never starts one. Starting is `yardr upgrade`'s own step.

## What each part says

`server` answers on this build. This implies the store opened and
migrated — the server opens the store before it listens, and a migration
that fails never listens — so there is no separate migration probe. A
server on another build fails with a line naming it. A server that does
not answer fails with why: `the server is not running` when the socket
is gone, else the dial error. The check never starts one.

`daemon` holds its lock on this build, and completed a pass on this
build since `--since`. The lock is probed the way `yardr status` probes
it, at the run's opening and again at its end: a daemon that flapped in
between (a crash loop under its service manager) fails. The build is the
`to` of the latest `daemon_replaced` or `daemon_started` event; a daemon
whose build is in neither — started fresh before `daemon_started`
existed and never replaced since — fails with a line saying to restart
it, not with a guess. The pass evidence is `<home>/daemon.pass`, which
the daemon stamps with its build and the time at the end of every
completed pass: the stamp must be on this build and at or after
`--since`, else the daemon fails as wedged. `--since` is a duration
back from now (`10m`, `90s`, the default `10m`) or the install's start
as an RFC3339 time, which the gate passes. A yard with no daemon
running skips the part instead of failing it. Where the daemon stopped
on a yard whose server still runs, the server judges the same staleness
live: the daemon's last completed pass or, if later, its last start is
over 3 minutes old with no daemon holding the lock. The server writes one
`daemon_lost` event and raises a run card for `yardr up`; a fresh
`daemon_started` ends it.

`views` probes every recorded web view with one GET of `/health`, and
counts each with the build it answers on. A view that answers on this
build passes; one that answers on another build fails where the yard
keeps its view as a login service, since the upgrade restarts that
service before the gate. Where the view was started by hand the line
names the restart instead (`view on <build>, started by hand: yardr web
stop && yardr web start`), and fails nothing: the upgrade leaves a
hand-started view alone. A view that 404s `/health` but answers `/` is
on a build from before the route: it is skipped with an `old:` line
naming the restart (`yardr web stop`, then `yardr web start`), not
failed. A view that answers nothing fails with a line naming its URL. A
yard with no web view skips the part.

## JSON

`--json` prints the same verdict as data: `ok` for the run, and each
part's `verdict` (`ok`, `fail`, `skip`) with the words after it as
`detail`, beside the facts.

```json
{
  "home": "~/.yardr",
  "build": "1904417",
  "ok": true,
  "server": {"verdict": "ok", "detail": "answers on 1904417, this build", "running": true},
  "daemon": {"verdict": "ok", "detail": "holds the lock on 1904417, this build, pass 12 s ago", "running": true},
  "views": {"verdict": "ok", "detail": "1 answers on 1904417", "views": [
    {"url": "http://127.0.0.1:8791/", "state": "answers", "build": "1904417"}
  ]}
}
```

A view's `state` is `answers`, `old` or `dead`.

## Rollback

When the bound expires with any covered yard not on the new build,
`yardr upgrade` rolls back: it copies `yardr.prev` over the installed
path once and [puts back the scripts](#the-scripts) it replaced, then,
in every covered yard, stops the daemon first and then the server —
`yardr down`'s order — and starts again only what ran before the
upgrade. Unlike `down` it stops no agent: agents already running keep
running through the restart, and their sessions are in the pre-install
snapshot, so the restored yard knows them.

Whether a store must go back too is answered by the old build itself,
per yard: it is started on the current store, and if it refuses with
the newer-schema error that yard's pre-install copy is put in the
store's place and it is started again. A yard that needed no restore
loses nothing. One that restored loses what the window held — bounded
by the timeout, minutes — so everything is told after the restore,
never only before it. Before a yard's copy is put in place, the upgrade
lists what the window held: the events since the head read with the
backup, reduced to the distinct beads they touch. The `rolled_back`
event data, the mails and the marker name each of those beads, so the
yardmaster re-checks them; a move such an agent repeats is refused by
revision and retried as today.

Whether the copies are restored at all is the upgrading yard's to say:
`upgrade_restore`, a boolean setting defaulting to true, with
`YARDR_UPGRADE_RESTORE` and a `--no-restore` flag overriding it for one
run. The one setting governs the whole run, every covered yard with it.
With restoring off, a failed check after a migration stops and tells
instead: each yard whose store the old build refuses stays down, and
the operator restores each by the documented recipe.

If the old build will not start even on the pre-install copy, that yard
stays down. The failure is durable on disk: `yardr upgrade` writes
`<home>/upgrade.failed` in the upgrading yard with the revision, the
stage, the reason, every covered yard and its outcome, each bead that
changed in the window, and who was already told outside the yard with
what each send answered, which `yardr doctor` reports as a FAULT until
the next successful upgrade logs the failure event and clears it.

## Telling

A failed install or a rollback is never silent while the yard answers.
Two event kinds say it: `install_failed` and `rolled_back`, both yard
events with no bead, of the class attention, so the yardmaster hears
them. The command also mails the yardmaster and the operator, and the
landed bead's note carries the one-line summary.

When the yard does not answer, the telling is twofold: the upgrade
sends the one message itself through each matched user's source,
reading the same subscriptions from the pre-install store copy — an
escape hatch for delivery, not a second configuration — and writes the
`<home>/upgrade.failed` marker with the per-user send record. During
the outage only users are told; the yardmaster is told when the yard is
back, by the stored event in its inbox as usual. The recovery logging
marks the event told only if every direct send succeeded; a failed send
leaves it untold, so normal delivery tells again once the yard is back.
A failed direct send is retried when the yard is back; if it never
comes back, the command's exit status, the terminal (hand run) or the
assembly's log (landing run), the marker and doctor carry it.
