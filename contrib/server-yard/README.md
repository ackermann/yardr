# contrib/server-yard

One script that sets a yard up on a fresh Linux server, from the steps of
[docs/install.md](../../docs/install.md#on-a-server-no-desktop), so that it
comes back after a reboot:

```
contrib/server-yard/server-yard --harness <kind> --operator <name>
```

On a machine that has yardr, herdr, git and a harness on PATH it:

1. writes herdr's server as a systemd user unit (`herdr.service`) and
   enables it, with the PATH of the shell it runs in, which the agents'
   panes inherit;
2. turns linger on, so the user's units run from boot to shutdown with
   nobody logged in;
3. creates the yard and starts it
   (`yardr init --harness <kind> --operator <name> --start`);
4. installs the daemon and the web view as login services;
5. ends with `yardr doctor`.

The harness login is the operator's step, in the yardmaster's pane once the
yard exists (`herdr agent attach <yard>-yardmaster`), and the script does
not wait for it: when `init --start` does not see the yardmaster come up,
the script goes on — the daemon starts it once the login is there. The last
word is doctor's, and so is the exit status: 0 when doctor names no fault,
1 when it still lacks something (on a fresh machine, the harness login).

A second run with the same flags changes nothing: each step looks first and
says `already:` where there is nothing to do. `--wait <duration>` is how
long the run waits for the yardmaster, as `yardr init --start` waits by
default without it.

What it does not do: log the harness in; move a changed PATH into services
that are loaded (`yardr daemon install` and `yardr web install` again do
that, as docs/install.md says); run where there is no systemd user session.
It writes one yard, the default home's.

Status: tested with the stand-ins below, on machines with a desktop; not
yet run on a fresh Linux server. That run is the trial's
(docs/design/2026-10-12-yardr-8wxe-yard-as-a-service.md, 3.7).

## The test and its stand-ins

`server_yard_test.go` runs the script twice: once with a harness that
answers, where the yardmaster comes up and doctor is clean, and the run
after changes nothing (the unit files byte for byte, no manager told, the
daemon and the server the same processes, no agent started); and once with
a harness that refuses every prompt, as one without its login does, where
the script goes on to the services and doctor all the same.

The test binary names itself to the servers its yards start (`TestMain`,
`storetest.NameTestBinary`: `YARDR_TEST_OWNER`, and `YARDR_TEST_HOME_CHECK`
for how often they look), and each yard's environment hands both on to the
script, and through the stand-in managers to the server and the daemon, which
stop within a few seconds of the binary's end. A run killed by its timeout
runs no cleanup, so that is what ends them. The herdr stand-in does not look
for the name: its process and the yard's directory stay until the test's
cleanup.

`fake/` holds what the script is run against, with `HOME`,
`XDG_CONFIG_HOME` and `YARDR_HOME` in a temp dir and nothing else of the
caller's environment:

- `herdr` (built from `main.go`): the API socket (`workspace.create`,
  `events.subscribe`) and the CLI the setup asks (`status server`,
  `agent list`, `agent start`, `agent prompt`, `agent get`, `pane read`),
  with one canned agent that is always working. With a file `logged-out`
  in its state dir a prompt is refused as herdr refuses one for an agent
  at a login screen. Every call goes to `calls.log`, every start to
  `agents.log`.
- `claude`: answers its version flag, and a one-shot call with `ok`,
  or with a login failure under `logged-out`.
- `systemctl`, `loginctl`: units and linger as files, every call logged.
  They model the manager starting a unit: herdr's server and the daemon
  really start, detached. The web view's unit is only marked: starting
  it would bind 127.0.0.1:8791.
- `launchctl`: the same for the test on macOS, where yardr writes
  launchd agents.

`managers/` installs the three service managers and holds which variable each
reads its state directory from. The yard-shape matrix of
[contrib/isolated-yard](../isolated-yard/README.md#the-yard-shape-matrix) uses
it too, to run this script in a yard that has a real herdr and a stand-in
agent that works beads.
