# contrib/server-yard

One script that sets a yard up on a fresh Linux server, from the steps of
[docs/install.md](../../docs/install.md#on-a-server-no-desktop), so that it
comes back after a reboot:

```
server-yard --harness <kind> --operator <name>
```

A release archive holds it beside `yardr` (`tar -xzf <archive> server-yard`);
a checkout has it as `contrib/server-yard/server-yard`.

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

Run it as the yard's account (a regular user with a home and a shell), in a
session of that account: by ssh or `sudo -iu <account>`. It asks nothing:
`yardr init` gets `/dev/null` on its stdin, since at a terminal it would stop
to ask whether to make one real call to the harness (a model call), which a
script that goes on without the harness login cannot wait for. `yardr doctor
--try` is that call, for when the login is there.

Where a step cannot be taken it stops with exit status 1 and says what to
do, the first two before it writes anything and the last before it creates the
yard:

- a program is not on PATH: the ones that are in `~/.local/bin`, which a
  shell without a profile (`ssh host server-yard`) lacks, are named apart,
  with the way on (a login shell);
- `systemctl --user` reaches no session of the account (`sudo -u` and `su`
  without a dash give none);
- linger is refused to the account (a server without polkit lets only root
  turn it on): `loginctl enable-linger <account>` as root, and the run again.

The harness login is the operator's step, in the yardmaster's pane once the
yard exists (`herdr agent attach <yard>-yardmaster`), and the script does
not wait for it: when `init --start` does not see the yardmaster come up,
the script goes on — the daemon starts it once the login is there. The last
word is doctor's, and so is the exit status: 0 when doctor names no fault,
1 when it still lacks something (on a fresh machine, the harness login: see
the run below).

A second run with the same flags changes nothing: each step looks first and
says `already:` where there is nothing to do. `--wait <duration>` is how
long the run waits for the yardmaster, as `yardr init --start` waits by
default (two minutes) without it; on a machine with no login yet that wait
can only run out, and `--wait 20s` shortens it.

What it does not do: log the harness in; move a changed PATH into services
that are loaded (`yardr daemon install` and `yardr web install` again do
that, as docs/install.md says); run where there is no systemd user session.
It writes one yard, the default home's.

## A run on a fresh server

Run on 22 October 2026 (yardr-zymt, for the team server of
docs/design/2026-10-22-yardr-m06d-team-server.md 3.7), following
docs/install.md, "From a release archive" and "On a server".

- **The machine**: Debian GNU/Linux 12.15 (bookworm), arm64, systemd 252: an
  OrbStack machine (`orb create debian`), with systemd as init as on a real
  box, and OrbStack's kernel (7.0.14), shared with its other machines. The
  image has curl, sudo and polkitd; `apt install git openssh-server` was the
  only addition.
- **The account**: `useradd --create-home --shell /bin/bash yardr`, no
  password, no sudo, an ssh key; root turned linger on once
  (`loginctl enable-linger yardr`). Everything below was typed as `yardr`,
  over ssh.
- **The archive**: `contrib/release/build v0.0.0-zymt` with
  `YARDR_RELEASE_TARGETS=linux/arm64`, the script of the release workflow,
  on the tree of this change: `yardr_0.0.0-zymt_linux_arm64.tar.gz` and
  `checksums.txt`, copied to the account's home with scp. The `server-yard`
  in it is the one of this directory (sha256 184245c0...), byte for byte. No Go
  on the machine, no `go install`.
- **The programs**, all in `~/.local/bin` of the account, where their
  installers put them: yardr out of the archive, herdr 0.9.4 (the release
  binary, `herdr-linux-aarch64`, its sha256 checked against the one
  herdr.dev/latest.json gives), Claude Code 2.1.308 (Anthropic's installer,
  claude.ai/install.sh). No Claude login.

The steps:

```sh
sha256sum --ignore-missing -c checksums.txt        # yardr_0.0.0-zymt_linux_arm64.tar.gz: OK
tar -xzf yardr_0.0.0-zymt_linux_arm64.tar.gz yardr server-yard
mkdir -p ~/.local/bin && mv yardr ~/.local/bin/
# herdr and claude fetched into ~/.local/bin, as above; then a new login:
yardr --version                                    # v0.0.0-zymt and the build's revision
./server-yard --harness claude --operator ops --wait 20s
```

The script, in an ssh session with a terminal (a person's way), wrote
`herdr.service` and started herdr's server, found linger on, created the
yard, installed the daemon and the web view as user units, and ended with
doctor. Doctor named **one fault**, the Claude login:

```
  ok     herdr       /home/yardr/.local/bin/herdr, its server is running; ...
  ok     claude      /home/yardr/.local/bin/claude, the harness of the yard's default, ...: answers its version flag (2.1.308 (Claude Code))
  ok     server      running
  ok     daemon      running
  ok     yardmaster  yardmaster is running (herdr agent yardr-yardmaster)
  FAULT  crew        yardmaster is not logged in: run claude in another terminal and log in there; ...
```

**That fault is the run's one warning, and it is the login.** The run left
the login out (it takes a person and an account), so the exit status was 1;
nothing else of doctor was red. Doctor needs the login because herdr starts
the yardmaster in its pane and claude sits at its "Select login method"
screen: the pane is up, the harness is not working. The script does not do
that step (docs/install.md, step 4; `herdr agent attach yardr-yardmaster`).

Then, on the same machine:

- **A second run** with the same flags: five `already:` lines, the unit
  files byte for byte as before, the same processes.
- **A reboot** (`orb restart`, which restarts the machine's systemd and
  everything under it, not the kernel) with nobody logged in
  (`loginctl list-sessions` empty): within 25 seconds herdr's server, the
  daemon and the web view were running as units of the account's user
  manager, with the yardr server beside them, and about a minute after the
  boot the daemon started the yardmaster again, at its login screen.
  (Rebooted 45 seconds after the yardmaster's first start, the first attempt
  after the boot counted as a failed start, "gone within 1m0s", and was tried
  again by the daemon on its next pass.)

### What broke, and what was fixed

Each was found by the run. The fixes to the script and the archive have
tests (`server_yard_test.go`, `contrib/release/build_test.go`); the page's
have none.

1. **`mv yardr ~/.local/bin/` fails on a new account**, which has no such
   directory (docs/install.md): now `mkdir -p ~/.local/bin && mv ...`, with
   the note that a login shell starting before the directory existed has no
   PATH for it.
2. **The script the page names was not in the archive.** An operator handed
   the archive alone had no `server-yard`: it is in a private repository.
   `contrib/release/build` now packs it beside `yardr`
   (`contrib/release/build_test.go` unpacks it and compares it with the
   tree's).
3. **At a terminal, `yardr init --start` stops with "Check that claude
   answers?"** and waits for a person, so the script hung in the way an
   operator runs it. The suite runs with no terminal and never saw it. The
   script now gives init `/dev/null` for stdin (a shim in front of `yardr`
   in the test says what init's stdin was).
4. **With no systemd user session** (`sudo -u yardr ./server-yard`) the script
   rewrote the unit file and then ended on `Failed to connect to bus: No
   medium found`. It now looks for a session before it writes anything and
   says how to get one.
5. **A server without polkit refuses linger to the account itself**
   (`Could not enable linger: Access denied`, exit 1); the script ended there
   with only that line. It now says that root runs `loginctl enable-linger
   <account>` and the script again. (On an image with polkitd, as this
   machine's, the account turned linger on itself, over ssh and from `sudo
   -iu`; the failure was made by removing polkitd.)
6. **From a shell without a profile** (`ssh host ./server-yard`) it said the
   programs were not on PATH and to install them, though they were in
   `~/.local/bin`. It now names the ones that are there and says to run it
   from a login shell.
7. **docs/install.md**: the account to use (a regular user with a shell, by
   ssh or `sudo -iu`, never `sudo -u`), git as a prerequisite, the web view
   the script installs and the steps did not name, and where this run is
   recorded.

### Tried, and not

- Reaching the account: ssh with and without a terminal, `sudo -iu` and
  `su -` (each gives `XDG_RUNTIME_DIR` and a working `systemctl --user`;
  the run itself was by ssh), `sudo -u` (neither; the script stops, and with
  `XDG_RUNTIME_DIR=/run/user/$(id -u)` exported it goes on).
- An account whose shell is `/usr/sbin/nologin`: herdr's panes start
  `/bin/bash` all the same and the yardmaster's claude ran; only a person
  cannot log in as that account.
- Not tried (left out of the bead): a real claude call, a Claude login, other
  distros (Debian 13, Ubuntu), amd64, a proxy, the network steps. What the
  run saw on the network, for them: herdr's server calls herdr.dev when it
  starts (`latest.json`, `agent-detection/*.toml`; not tried: whether it
  starts without a route), and the installers call github.com and
  downloads.claude.ai.

## The test and its stand-ins

`server_yard_test.go` runs the script three times over: once with a harness
that answers, where the yardmaster comes up and doctor is clean, and the run
after changes nothing (the unit files byte for byte, no manager told, the
daemon and the server the same processes, no agent started) and `yardr init`
was given a closed stdin; once with a harness that refuses every prompt, as
one without its login does, where the script goes on to the services and
doctor all the same; and once for each place it stops with the way on (the
programs only in `~/.local/bin`, no systemd session, linger refused).

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
  it would bind 127.0.0.1:8791. A file `no-session` in systemctl's state
  dir makes every call fail as for an account with no session, a file
  `denied` in loginctl's refuses `enable-linger`.
- `launchctl`: the same for the test on macOS, where yardr writes
  launchd agents.

`managers/` installs the three service managers and holds which variable each
reads its state directory from. The yard-shape matrix of
[contrib/isolated-yard](../isolated-yard/README.md#the-yard-shape-matrix) uses
it too, to run this script in a yard that has a real herdr and a stand-in
agent that works beads.
