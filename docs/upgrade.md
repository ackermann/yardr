# Upgrading yardr

`yardr upgrade --check` asserts the yard runs this build — the build of
the `yardr` that was asked — read-only. It is the install gate, by hand:
a bare `yardr upgrade` will install the depot's build, verified, or roll
back (**planned**).

```sh
$ yardr upgrade --check
server    ok: answers on 1904417, this build
daemon    ok: holds the lock on 1904417, this build, pass 12 s ago
views     ok: 1 answers (http://127.0.0.1:8791/); 1 old: restart it (yardr web stop, then yardr web start) (http://127.0.0.1:8792/)
```

One line per check, exit 1 when any part fails, 0 otherwise.

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
