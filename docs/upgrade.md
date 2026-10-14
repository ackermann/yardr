# Upgrading yardr

`yardr upgrade` installs the depot's build, verified, or rolls back. It
does the hand step whole: from the checkout of the depot the
`upgrade_from` setting names it runs `go install .`, checks every
covered yard runs it, and otherwise puts the previous build back. The
assembly calls it after the merge when the setting names the depot; by
hand it is run after pulling instead of `go install .`. A yard without
the setting is untouched.

```sh
$ yardr upgrade
backup    /Users/jo/.yardr/backups/yard.db.before-install
keep      /Users/jo/go/bin/yardr.prev (1904410)
install   go install . at 1904417
health    ok in 24 s
```

One line per step, exit 1 when the install fails, 0 when every covered
yard runs the new build.

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
left running what was started; nothing else is started. A yard the gate
cannot check fails it and is named in the marker and the message.

`yardr upgrade --check` is the gate by hand: it asserts the yard runs
this build — the build of the `yardr` that was asked — read-only.

```sh
$ yardr upgrade --check
server    ok: answers on 1904417, this build
daemon    ok: holds the lock on 1904417, this build, pass 12 s ago
views     ok: 1 answers (http://127.0.0.1:8791/); 1 old: restart it (yardr web stop, then yardr web start) (http://127.0.0.1:8792/)
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
running skips the part instead of failing it.

`views` probes every recorded web view with one GET of `/health`. A
view that answers — whatever the status — runs, and passes: views do
not replace themselves, so this checks that they answer, not that they
run anything new. A view that 404s `/health` but answers `/` is on a
build from before the route: it is skipped with an `old:` line naming
the restart (`yardr web stop`, then `yardr web start`), not failed. A
view that answers nothing fails with a line naming its URL. A yard with
no web view skips the part.

## JSON

`--json` prints the same verdict as data: `ok` for the run, and each
part's `verdict` (`ok`, `fail`, `skip`) with the words after it as
`detail`, beside the facts.

```json
{
  "home": "/Users/jo/.yardr",
  "build": "1904417",
  "ok": true,
  "server": {"verdict": "ok", "detail": "answers on 1904417, this build", "running": true},
  "daemon": {"verdict": "ok", "detail": "holds the lock on 1904417, this build, pass 12 s ago", "running": true},
  "views": {"verdict": "ok", "detail": "1 answers (http://127.0.0.1:8791/)", "views": [
    {"url": "http://127.0.0.1:8791/", "state": "answers", "build": "1904417"}
  ]}
}
```

A view's `state` is `answers`, `old` or `dead`.

## Rollback

When the bound expires with any covered yard not on the new build,
`yardr upgrade` rolls back: it copies `yardr.prev` over the installed
path once, then, in every covered yard, stops the daemon first and then
the server — `yardr down`'s order — and starts again only what ran
before the upgrade. Unlike `down` it stops no agent: agents already
running keep running through the restart, and their sessions are in the
pre-install snapshot, so the restored yard knows them.

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
