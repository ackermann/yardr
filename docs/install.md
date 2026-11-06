# Installing yardr

yardr is one static binary, and `install.sh` is the one way to install it, on
macOS and Linux, arm64 and amd64. It puts `yardr` in `~/.local/bin`, and the
script is the only definition of where yardr goes. Then yardr needs git, herdr
and an agent harness beside it, and `yardr doctor` says which of those are
missing.

```sh
curl -fsSL https://yardr.dev/install.sh | sh
```

To read the script before it runs, download it and run the file:
`curl -fsSLO https://yardr.dev/install.sh`, then `sh install.sh`. A contributor
installs the checkout instead, with `./install.sh --from-source`
([From a checkout](#from-a-checkout)).

## What install.sh does

With no flag it installs the latest release. [From a release
archive](#from-a-release-archive) takes the same steps by hand:

1. It reads `https://yardr.dev/releases/latest.json` for the version and the
   archive of this system: macOS or Linux, arm64 or amd64, as `uname` says it,
   except that a Mac's shell started under Rosetta is taken for the Apple
   silicon Mac it runs on.
2. It requests each file directly from yardr.dev over https, without following
   redirects, and stops unless the response is 2xx. What `latest.json` names
   has to be a plain path under `/releases/`, or the script stops.
3. Where `minisign` is on PATH, it checks the signature on `checksums.txt`
   against yardr's release key ([Signed checksums](#signed-checksums)) before it
   fetches the archive. Where it is not, the script says that only the checksum
   was checked, which shows the download is whole and not who made it. Install
   [minisign](https://jedisct1.github.io/minisign/) (`brew install minisign` on
   macOS, `apt install minisign` on Debian) and run the script again to check
   who did.
4. It checks the archive against its line in `checksums.txt`. A signature or a
   checksum that does not check ends the script with nothing written to
   `~/.local/bin` and nothing left in the temporary directory (`$TMPDIR`, else
   `/tmp`).
5. It makes `~/.local/bin` if there is none, writes `yardr`, the one member it
   unpacks, to a temporary file there, runs it with `--version`, and renames it
   into place. A yardr that is running goes on with the file it runs from while
   the server and daemon notice the new one ([Upgrading](#upgrading)), and a
   program that does not run replaces nothing. The script ends by printing what
   `yardr --version` said: the release's tag and revision.
6. Where `~/.local/bin` is not on PATH, it says the one line to add to the
   shell's startup file (`~/.zshrc`, `~/.bashrc` or `~/.profile`), and writes
   no profile itself:

   ```sh
   export PATH="$HOME/.local/bin:$PATH"
   ```

   Where another `yardr` comes first on PATH, it says that too, as that is the
   one a shell then runs.
7. Where `herdr` is not on PATH, it says so in one line, prints herdr's own
   installer ([herdr.dev](https://herdr.dev)) as a line to run, and says that
   yardr also needs an agent such as claude or codex. Where herdr is on PATH
   it says nothing of it. It only looks, with `command -v`: it runs nothing
   and installs nothing.

   ```sh
   curl -fsSL https://herdr.dev/install.sh | sh
   ```

It needs `curl`, `tar`, `awk` and `sha256sum` or `shasum`, and is plain POSIX sh.
It writes `~/.local/bin/yardr` and nothing else of yours, and there is no flag
for another directory. `server-yard`, the other program an archive holds, is
not unpacked: [On a server](#on-a-server-no-desktop) says where to take it
from.

## From a release archive

What install.sh does, by hand: for a machine that should not pipe a script to a
shell, and for `server-yard`.

Each release has four archives, `yardr_<version>_<os>_<arch>.tar.gz` for
darwin/arm64, darwin/amd64, linux/amd64 and linux/arm64. An archive holds
`yardr`, `server-yard` (the script of [On a server](#on-a-server-no-desktop)),
the README, and the licence (FSL-1.1-ALv2) as `LICENSE` and `NOTICE`.
`checksums.txt`, with the sha256 of each archive and of `install.sh`, and
`install.sh` itself are files of the release beside the archives, not inside
them: download `checksums.txt` with the archive.

Releases are public at yardr.dev: `https://yardr.dev/releases/<tag>/`
holds the four archives, `checksums.txt` and its signature. The current tag
is the `version` in `https://yardr.dev/releases/latest.json`, or the tag in
the invite reply. (The archives name the version without the tag's leading
`v`: tag `v1.2.3` is archive `yardr_1.2.3_…`.)

```sh
tag=v1.2.3                 # the current tag, from latest.json
version=${tag#v}
target=darwin_arm64        # or darwin_amd64, linux_amd64, linux_arm64
curl -fSLO https://yardr.dev/releases/$tag/checksums.txt
curl -fSLO https://yardr.dev/releases/$tag/yardr_${version}_$target.tar.gz

# verify: the line for the archive must say OK
shasum -a 256 --ignore-missing -c checksums.txt      # Linux: sha256sum --ignore-missing -c checksums.txt

# unpack, and put the binary on PATH
tar -xzf "yardr_${version}_$target.tar.gz" yardr
mkdir -p ~/.local/bin && mv yardr ~/.local/bin/     # any directory on your PATH
yardr --version            # the release's tag and revision; yardr version prints the same
```

`--ignore-missing` leaves out the lines of the files you did not download: the
other three archives, and `install.sh`. A copy of `install.sh` downloaded beside
`checksums.txt` is checked by the same command, against its own line.

A new account has no `~/.local/bin` until the `mkdir`, and Debian's
`~/.profile` puts it on the PATH of the login shells that start after it
exists: log in again, or run `. ~/.profile`, before `yardr --version`.

`checksums.txt` comes from the same release as the archive, so it shows that
the download is whole, not who made it. A release whose checksums were
signed ([Signed checksums](#signed-checksums)) has `checksums.txt.minisig`
beside them, a [minisign](https://jedisct1.github.io/minisign/) signature
that shows who made them, to whoever has the release key's public half.
Download it with the other two and
verify it before the checksums are believed:

```sh
curl -fSLO https://yardr.dev/releases/$tag/checksums.txt.minisig
minisign -V -P RWSzUPGBbv7MDqH+nnbNmYdOm20UTkuk7eC5e6XuVtYRwZtaEZRlV/WB -m checksums.txt
```

The key after `-P` is yardr's release key (id `0ECCFE6E81F150B3`), whose
secret half stays with whoever cuts the releases; a release before v0.10.1
may have no signature.

## macOS

A release's darwin binaries are signed with an Apple Developer ID and
notarized by Apple when the release was built on a Mac that has the identity
([Making a release](#making-a-release)), and not otherwise: the workflow
alone builds them unsigned. The binary says which it is:

```sh
codesign -dv --verbose=2 ~/.local/bin/yardr
```

A signed one prints `Authority=Developer ID Application: …` and
`Identifier=dev.yardr.yardr`. An unsigned arm64 binary is signed ad hoc by
the Go linker, exactly like a build from a checkout (`Signature=adhoc`), and
an unsigned amd64 binary carries no signature at all.

What macOS does with an unsigned binary depends on how it arrived. `curl`
does not mark what it downloads, and the binary runs. A browser marks
the archive as quarantined (`com.apple.quarantine`), and an unpacker can
pass the mark on to `yardr` (Finder's does); Gatekeeper then refuses to run
it, as software Apple cannot check. Clear the mark:

```sh
xattr -d com.apple.quarantine ~/.local/bin/yardr
```

`xattr ~/.local/bin/yardr` lists the marks a file has; no
`com.apple.quarantine` in it means there is nothing to clear.

A signed and notarized binary runs however it arrived, with nothing to
clear. One thing is left: the proof of notarization (Apple's ticket) can be
attached to an app, a disk image or an installer package, not to a bare
binary, so the archive does not carry it. The first time a quarantined
`yardr` starts, Gatekeeper asks Apple for the ticket, which needs the
network once; started first on a Mac that is offline, it is refused as an
unsigned one is, until the Mac is online or the mark is cleared as above.
The signature says who built the binary. `checksums.txt` is of the archive
with the signed binary in it, and still says only that the download is
whole.

## From a checkout

A contributor installs the checkout with the same script. It needs Go, the
toolchain `go.mod` names ([how to install it](https://go.dev/doc/install)):

```sh
./install.sh --from-source
```

It runs `go build` in the checkout the script sits in, and installs the result
as a release's `yardr` is installed: a temporary file in `~/.local/bin`, run
with `--version`, renamed into place. Nothing is downloaded. `yardr --version`,
or `yardr version`, prints its revision (`abcdef123456`, with `-modified` for a
build from a tree with uncommitted changes).

Where the binary sits is no condition of upgrading it from source later:
[`yardr upgrade`](#upgrading) writes to the binary it runs from, wherever that
is.

## What yardr doctor still asks for

`install.sh` installs nothing but yardr. Run `yardr doctor`
([doctor.md](doctor.md)): it works before there is a yard and names what is
missing, with where to get it:

- **git**, on PATH;
- **herdr**, on PATH and with its server running: it runs the agents' panes
  ([herdr's server](#herdrs-server));
- **a harness**: an agent herdr starts (claude, codex, opencode, …), on PATH
  and answering its version flag. For Claude Code and Codex the answer has to
  say who it is: a program called `claude` that prints something else is not
  Claude Code, and doctor does not count it
  ([doctor.md](doctor.md#a-program-that-answers-as-another)). That is not yet a
  harness that works: `yardr doctor --try` makes one real call to it, which
  costs a model call and says when it is logged out
  ([doctor.md](doctor.md#--try-one-real-call-to-each-harness)).

A program doctor reports as not on PATH can be installed all the same: over
ssh with a command, and in a service, the shell reads no profile and lacks
what yours adds (`~/.local/bin`, where claude and codex install themselves).
Doctor asks your login shell and names the directory when that finds it.

### The onboarding offers to install herdr

`yardr` in a terminal on a home with no yard runs the onboarding
([configuration.md](configuration.md#creating-a-yard-the-onboarding-and-yardr-init)).
Where herdr is not on PATH, and yardr pins a download for the system (macOS and
Linux, on arm64 and amd64), it asks `Install herdr 0.9.4 into ~/.local/bin
now?`, and yes is the default. On a yes it:

1. downloads the file herdr publishes for the system, from the address yardr
   pins (GitHub's release, which answers with a redirect to GitHub's storage:
   only a redirect to https is followed);
2. compares its size and its sha256 with the ones yardr pins, which are not
   read from herdr's site at the time ([doctor.md](doctor.md#a-program-fetched-on-a-mac)
   has them and where they were verified from);
3. writes it to a temporary file in `~/.local/bin`, runs it with `--version`,
   and renames it to `~/.local/bin/herdr`, as `install.sh` does for yardr;
4. uses `~/.local/bin` for the rest of that run, so that the onboarding and the
   daemon it starts find herdr, and, where it was not on PATH, prints the line a
   new terminal needs, the one `install.sh` prints. It writes no shell profile.

A download that fails, a size or sha256 that differs, or a file that does not
answer `--version` keeps nothing, says why in one line, and falls back to the
command above, as a no does. herdr's own installer is never run, nor any
script: only the pinned file, once it checks out. A herdr already at
`~/.local/bin/herdr` is used as it is and never replaced (`herdr update` keeps
it current); the onboarding then only puts the directory on PATH for the run.
Without a terminal, nothing is offered and the command is printed, as
`install.sh` prints it. A harness (claude, codex) is not offered this way.

Then run `yardr` for the onboarding ([the README](../README.md#quick-start)).
It ends in [`yardr chat`](chat.md#from-the-onboarding), talking to the
yardmaster in the terminal you ran it in.

From then on `yardr up` starts everything the yard needs that is not
running, herdr's server included, and `yardr down` stops the yard and its
agents: [Starting and stopping a yard](configuration.md#starting-and-stopping-a-yard).

## herdr's server

herdr on PATH is a program; what starts agents is its server. Without one
every start fails with herdr's `server_not_running`, and `yardr doctor` says
`FAULT herdr … its server is not running`.

- `yardr up` starts it when it does not answer: herdr's headless server,
  in the background ([Starting and stopping a yard](configuration.md#starting-and-stopping-a-yard)).
  `yardr down` leaves it running.
- On a machine you sit at, run `herdr` in a terminal: it launches the
  session, and its server stays when you detach (`ctrl+b q`). `herdr server
  stop` ends it, and its panes with it.
- On a machine without a desktop, `herdr server` runs the headless server,
  which herdr's [CLI reference](https://herdr.dev/docs/cli-reference/)
  recommends for supervised or service-style setups. Run it under what keeps
  a program running there.

To keep it running from login on, `yardr up --login` installs a login
service for it, and for the daemon
([Keep it running after a reboot](#keep-it-running-after-a-reboot)).

`yardr daemon install` installs yardr's daemon, not herdr's server, and
`yardr up` without `--login` starts herdr's server without installing
anything: after a reboot it is `yardr up` again.

## Keep it running after a reboot

Three things keep a yard going, and a reboot stops all three: herdr's
server, which starts the agents' panes, yardr's daemon, and the web view if
you use it. One command keeps them running from login on:

```sh
yardr up --login
```

It starts what does not run, as `yardr up` does, and installs a login
service for each: a launchd agent on macOS, a systemd user unit on Linux
([configuration.md](configuration.md#login-services)).

| part | its login service |
|---|---|
| herdr's server | `dev.yardr.herdr.plist` in `~/Library/LaunchAgents/`, `yardr-herdr.service` in `~/.config/systemd/user/`: the two files below, written by yardr, with the herdr your login shell finds, that shell's PATH and, when it has any, its proxy and CA variables. One for the user, not for the yard |
| the daemon | what `yardr daemon install` writes |
| the web view | what `yardr web install` writes, only when the yard's `web_addr` setting names its address. Without the setting it stays its own command: `yardr web install` |

`yardr down --login` stops the yard and removes them again. `yardr status`
says which is installed (`login service for its server: not installed
(yardr up --login)` under `herdr`), and `yardr doctor` lists the same under
`Optional`.

- **One herdr server serves every yard on the machine**, so its service is
  the user's. A second yard's `yardr up --login` finds it, says `installed
  already` and installs its own daemon's alone. `yardr down --login` in one
  yard leaves herdr's service while another yard's daemon still starts at
  login, and names that yard; the last yard's removes it.
- **A service of your own is left as it is.** A file in the service
  directory (or the machine's: `/Library/LaunchAgents`,
  `/Library/LaunchDaemons`, `/etc/systemd/user`, `/etc/systemd/system`) that
  runs `herdr server` and that yardr did not write, under any name, is
  yours: `up --login` says which file it found and writes none beside it,
  and `down --login` never touches it. That covers the two files below
  saved by hand and the unit `contrib/server-yard/server-yard` writes. A
  service its manager knows from a file elsewhere is not found.
- **A server that runs now is not restarted.** Stopping it would end its
  panes, and a second copy beside it would exit against the socket. The
  file is written, launchd loads it at your next login, and systemd is told
  to start it then (`enable`, without `--now`). With no server running,
  the service is loaded at once and starts it.
- **`yardr down --login` never stops herdr's server** either: it removes
  the file and tells systemd not to start it again (`disable`, without
  `--now`). What runs runs on until you log out or run `herdr server stop`.
- **It restarts on failure only.** `herdr server stop` is not fought: a
  stopped server stays stopped until the next login or the next `yardr
  up`. `herdr update` replaces herdr's program where the service's file
  names it, so nothing is to be installed again: the server that runs keeps
  the old program until it is stopped, and the service starts the new one
  from then on. A herdr that moves to another path needs `yardr down
  --login` and `yardr up --login` once more.
- **A yard that names its herdr session** (the `herdr_session` setting)
  gets the service of that session's server, `dev.yardr.herdr.<session>` or
  `yardr-herdr-<session>.service`, with `HERDR_SESSION` and
  `HERDR_SOCKET_PATH` in it.
- On Linux, `loginctl enable-linger "$USER"` lets your services run with
  nobody logged in; yardr does not run it.

`yardr up --login --json` says the same as data, under `login`: each
service with `part`, `result` (`installed`, `found` for one of yardr's that
was there, `left` for one that is not yardr's), `file`, `loaded`, `foreign`
(the files that are not yardr's) and `error`.

### A service file of your own

On a server, or when you want the file to be yours, save one by hand.
yardr then finds it and leaves it alone.

On macOS save this as `~/Library/LaunchAgents/herdr-server.plist`, with
the path `command -v herdr` prints in your login shell for
`/usr/local/bin/herdr`, and the PATH of that shell for the example:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>herdr-server</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/herdr</string>
		<string>server</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/Users/you/.local/bin:/opt/homebrew/bin:/usr/bin:/bin</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
</dict>
</plist>
```

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/herdr-server.plist
herdr status server                # status: running, and its socket
```

On Linux save this as `~/.config/systemd/user/herdr.service`, with the
same two replacements:

```ini
# ~/.config/systemd/user/herdr.service
[Unit]
Description=herdr server

[Service]
ExecStart=/usr/local/bin/herdr server
Environment=PATH=/home/you/.local/bin:/usr/local/bin:/usr/bin:/bin
Restart=on-failure

[Install]
WantedBy=default.target
```

```sh
systemctl --user daemon-reload && systemctl --user enable --now herdr
loginctl enable-linger "$USER"     # so it runs with nobody logged in
herdr status server                # status: running, and its socket
```

These are what `yardr up --login` writes too, with a first line that says
so, the user's home as working directory and, on macOS, a log in
`~/Library/Logs/` (`TestHerdrServiceFilesGolden` holds both). herdr's own
documentation is what holds for its server. The unit is also what
`contrib/server-yard/server-yard` writes (the values quoted as systemd
needs, and the proxy and CA variables of its environment as below), and its
test holds it.

- The path of herdr is absolute in both: a service reads no profile, so
  `herdr` alone is not found. `command -v herdr` in your login shell
  prints it.
- The PATH is the full one of that shell: the agents' panes inherit the
  server's environment, and what launchd and systemd give it without one
  is minimal (`~/.local/bin`, where harnesses install themselves, is not
  in it). If a harness is then not found in a pane, the PATH is what to
  fix.
- Behind an egress proxy the file holds the proxy and CA variables too,
  as the PATH is held: an `Environment=` line each in the unit, a key each
  under `EnvironmentVariables` in the plist. `yardr up --login` writes the
  ones its shell has, and `contrib/server-yard/server-yard` the ones its own
  environment has. The agents' panes inherit them from herdr's server
  ([configuration.md](configuration.md#behind-an-egress-proxy)). A proxy's
  address can hold a password, so both write the file mode 0600; a file of
  your own wants `chmod 600` too.
- Both restart it on failure only: a `herdr update`, which replaces the
  binary, and `herdr server stop` are not fought, and a stopped server
  stays stopped until the next login.
- Load either when no herdr server runs: beside one that does, the
  service starts a second copy that exits against the socket. After a
  reboot none runs; when one runs now, the file still takes effect at
  the next login, so leave the running one alone (stopping it ends its
  panes).
- A LaunchAgent loads at login: on a headless Mac with nobody logged in
  after a reboot, nothing starts. Turn on automatic login for the user
  (System Settings), so the login, and the server with it, happens on
  its own.

## On a server (no desktop)

A machine reached over ssh, with nobody at a terminal of its own. The order
matters: each step is what the next one needs. An agent doing this for
someone reads [agents.md](agents.md) first.

The yard runs as an account of its own: a regular user with a home and a
shell (`useradd --create-home --shell /bin/bash yardr`), reached by ssh or
`sudo -iu yardr`. `sudo -u yardr` is no way in: it leaves the account without
a systemd session, and `systemctl --user` has nothing to talk to. The machine
needs git beside yardr, herdr and the harness (`apt install git` on Debian),
and a person with root once, for step 3 where there is no polkit.

One script does these steps, and a second run of it changes nothing:
`server-yard --harness <kind> --operator <name>`, which a release archive holds
beside `yardr` (`install.sh` unpacks `yardr` alone: download the archive as
[From a release archive](#from-a-release-archive) does, and run
`tar -xzf <archive> server-yard`) and a checkout has as
`contrib/server-yard/server-yard`
([its README](../contrib/server-yard/README.md), with a run of it on a fresh
Debian server). What follows is what it does, step by step.

1. **Put the programs where every shell finds them.** yardr, herdr and the
   harness have to be on the PATH of three shells: yours, a shell without a
   terminal (`ssh host yardr doctor`), and a systemd user service. The last
   two read no profile, and on many systems `~/.local/bin` is on PATH only
   through `~/.profile`. Either move the programs to a directory all three
   have (`/usr/local/bin`; `install.sh` writes `~/.local/bin` alone), or keep
   `~/.local/bin` and name it where it is missing: `yardr doctor` says which
   program your login shell finds that the shell it runs in does not, and in
   which directory.

2. **Run herdr's server as a service**, with the systemd user unit under
   [A service file of your own](#a-service-file-of-your-own). Nothing
   starts an agent before it runs. (`yardr up --login` writes the same unit
   once there is a yard; here the server has to run before there is one.)
   The unit the script writes carries what its environment had: the PATH
   and, behind an egress proxy, the proxy and CA variables, which the
   agents' panes inherit; it is mode 0600, as a proxy's address can hold a
   password. By hand, put the ones your shell has in the unit.

3. **Let your services run with nobody logged in:**
   `loginctl enable-linger "$USER"`. Without it a systemd user unit ends
   with your last ssh session
   ([configuration.md](configuration.md#login-services)). A server without
   polkit refuses the account this for itself (`Could not enable linger:
   Access denied`): a person with root runs `loginctl enable-linger <account>`
   once, and the account goes on from there.

4. **Check that the harness works, not only that it is installed.** Log in
   to it as its own documentation says, then:

   ```sh
   yardr doctor --try     # one real call to each harness: costs a model call
   ```

   `ok <harness> tried: one real call answered` is the line to wait for. A
   harness's own status is not that: on the install this part comes from,
   `codex login status` said logged in and a real call was refused.
   `--try` runs the call with stdin closed, as a service would; by hand that
   is `codex exec --skip-git-repo-check 'Reply ok' < /dev/null`
   ([the call for each kind](doctor.md#--try-one-real-call-to-each-harness)).
   It does not find a harness too old for the flags herdr starts it with:
   that shows when the yardmaster's start fails. `yardr init --start` then
   prints the last lines of its pane, and `yardr doctor` the reason of the
   last failed start. Update the harness then.

5. **Create the yard and install the daemon**, from a login shell (an ssh
   session is one; `ssh host cmd` is not):

   ```sh
   yardr init --harness codex --start     # no form: flags only, see yardr init --help
   yardr daemon install                   # a systemd user unit, started now and at boot
   yardr web install                      # the web view the same way, on 127.0.0.1:8791 (the script does this too)
   yardr doctor
   ```

   `yardr daemon install` writes the PATH of the shell it runs in into the
   unit, and names the programs that PATH lacks. Run it again after the PATH
   changes ([configuration.md](configuration.md#login-services)). It writes
   the shell's proxy and CA variables too, on a box behind a proxy
   ([configuration.md](configuration.md#behind-an-egress-proxy)).

6. **Look at the yardmaster.** `yardr init --start` and `yardr doctor` print its
   herdr agent (`<yard>-yardmaster`, `yardr-yardmaster` for `~/.yardr`):

   ```sh
   herdr agent attach yardr-yardmaster     # its pane in this terminal; ctrl+b q detaches
   yardr chat                         # or talk to it by mail, in one view
   ```

   Both need a terminal: `ssh -t host herdr agent attach yardr-yardmaster` for
   one command over ssh.

On Linux the checksum line of [the download](#from-a-release-archive) is
`sha256sum --ignore-missing -c checksums.txt`. Without `--ignore-missing` it
fails on the three archives you did not download.

This part was written from one such install (v0.1.0, Ubuntu, 5 Oct 2026) and
from herdr's documentation, and the steps and the script were then run on a
fresh Debian 12 server from a release archive (the README of
contrib/server-yard records that run). The script is also tested with
stand-ins for herdr and the harness; yardr's tests run on no machine without a
desktop.

Where the yard is a team's, shared by several people on one box, read [A
team server](team-server.md) next: it starts from this section and adds the
network, the documents, the people, and upgrading by hand.

## A yard somewhere else

One install serves any number of yards: each is a directory, its home, and
`--home <dir>` on a command names it, with no variable to set:

```sh
yardr --home ~/yards/work                         # the onboarding, in a terminal
yardr --home ~/yards/work init --harness claude   # or scripted
yardr --home ~/yards/work doctor
yardr --home ~/yards/work daemon install          # a login service for that yard
```

Keep the directory's path short (the server's unix socket lives in it, and a
home too long for one is refused). A yard that takes webhooks next to another
needs an address of its own: `yardr --home ~/yards/work settings set
hook_addr 127.0.0.1:8791`
([configuration.md](configuration.md#yardrs-own-state)).

## Upgrading

Install the newer build over the old one, by any of three ways: run
`install.sh` again, which installs the latest release over it (`./install.sh
--from-source` does the same from a checkout), run `yardr upgrade` on a
depot's checkout, which builds it and writes the build over the binary it runs
from, wherever that is ([upgrade.md](upgrade.md#where-it-writes)), or, with
no depot named, run `yardr upgrade` and let it fetch the latest signed
release itself: it reads the release from yardr.dev, verifies its signature
with the release key built into the binary and checks the archive against
the signed checksums, with no checkout and no Go, and installs the proved
binary through the same checked gate
([upgrade.md](upgrade.md#from-a-signed-release)). The server and daemon notice
the new file and replace themselves
([configuration.md](configuration.md#upgrading)).

The ways mix: a release replaces a build from a checkout of an earlier commit,
and the other way round. A release installed over a newer build from a checkout
is allowed too, and the running server and daemon then skip it until they are
restarted ([configuration.md](configuration.md#upgrading)). `install.sh`
writes `~/.local/bin/yardr` and no other: a yardr installed somewhere else is
left where it is, and runs instead of the new one while it comes first on PATH,
which the script says. `command -v yardr` says which runs.

## Removing yardr

`yardr uninstall` takes a yard off the machine. It stops the yard, removes
what yardr installed to start it at login, and prints one list of what it
did not touch, with the path of each.

```sh
yardr uninstall                       # asks before it stops an agent at work
yardr uninstall --yes                 # a script: no question
yardr uninstall --json | jq '.left'   # what is left, as data
```

It is no second way to stop a yard or to remove a service:

1. It stops the yard as `yardr down` does, with the same warning, question
   and `--yes`
   ([configuration.md](configuration.md#starting-and-stopping-a-yard)).
2. It removes every login service yardr wrote for this yard, as
   `yardr daemon uninstall` and its siblings do: the daemon's, the web
   view's, the BDP server's and the telemetry export's. yardr's service for
   herdr's server is the user's, not the yard's: it goes with the last yard
   that starts at login, and while another yard of yours does, it is left
   and that yard named. A service for herdr's server that you saved
   yourself is never touched. herdr's server itself runs on.
3. It lists what is left.

Nothing of your work is deleted. What is left, and where:

| what | where | to remove it |
|---|---|---|
| the yard's home: the store with every bead, the roles, flows, packs and logs | `~/.yardr`, or the `--home` you gave | `yardr uninstall --purge` (below) |
| each depot | your repository or directory, where it was | yardr never deletes one |
| the `.yardr` directory of a depot: its roles, flows and test command | in the repository | delete it yourself, in a commit |
| the worktrees of the yard's beads | `<home>/worktrees/<depot>/<bead>`; the list says which has uncommitted changes | `git -C <repository> worktree remove <path>` |
| the branches under `yard/` | in each repository | `git -C <repository> branch -D <branch>` |
| yardr | the file the list names: uninstall does not delete itself | `rm <file>` |
| herdr | the file the list names; its server is left running | `herdr server stop` ends its server and every pane in it; the program is removed as it was installed |
| each harness the yard ran (`claude`, `codex`, ...) | the file the list names | as it was installed; its login and settings are its own |

`yardr uninstall` is safe to repeat. A yard that does not run and has no
login service is no error: it says so and prints the list. Nothing is
removed while nothing was stopped (a question you answered no, a daemon that
could not be stopped), and what could not be done is marked `NOT`, with exit
status 1. After it the yard is as `yardr down --login` leaves it, and
`yardr up` starts it again. On a home with no yard in it (one you deleted by
hand) it removes the login services that yard left, and stops nothing.

### Deleting the yard's home

`--purge` deletes the yard's home too: the store with every bead, mail and
note. It is the one thing here that deletes your work, so it takes the
yard's name, which `yardr status` prints (the `name` setting, else the
home's directory name):

```sh
yardr uninstall --purge               # names the directory, counts its beads, asks you to type the name
yardr uninstall --purge=work --yes    # with nobody to ask: both, or nothing happens
```

| who runs it | what `--purge` needs |
|---|---|
| a person at a terminal | the yard's name, typed when asked, or given as `--purge=<name>` |
| a script, stdin closed, or `--json` | `--yes` and `--purge=<name>`, both |

Anything else is refused before anything is stopped: no name, a wrong name,
or the right name without `--yes` where nobody can be asked. `--yes` alone
never deletes a yard, so no caller does it by a habit of passing `--yes`.
The name goes after `=`; `--purge work` is refused too. And it is the
operator's alone, as the settings, users and peers in the store are: an
agent or a crew member that runs it is refused the same way.

Even then it deletes the yard and nothing else of yours, and says what it
kept:

- a depot inside the home, as `yardr depot add --clone` puts one in
  `<home>/depots/`, stays whole, with every branch;
- a worktree with uncommitted changes stays, and so does one git cannot
  read, since nobody can say it has none. A worktree without changes is
  removed as `git worktree remove` does: its commits are on its branch, in
  the depot;
- the yardr program, when it lies in the home.

The home stays for what was kept, and the list says where each is. A home
that is your own home directory or lies inside a depot is refused: delete
what is the yard's in it by hand.

`yardr uninstall --json` prints `home`, `yard`, `name`, `beads`,
`uninstalled`, `confirm`, `down` (the answer of `yardr down --json`),
`login` (each service with `part`, `result`: `removed`, `absent` or `left`,
`file`, `detail`, `error`), `purge` with `--purge` (`home`, `beads`,
`deleted`, `gone`, `kept` with `path` and `why`, `error`) and `left`: each
thing with `kind` (`home`, `depot`, `yardr_dir`, `worktree`, `branches`,
`login_service`, `yardr`, `herdr`, `harness`), `name`, `path`, `detail`,
`uncommitted` for a worktree, `branches`, and `remove`. Without `--yes` on a
yard with any agent it stops nothing and prints `"confirm": true`, as
`yardr down --json` does. The exit status is 0 when `uninstalled` or
`confirm` is true, else 1.

## Making a release

### With the yard

A release bead cuts the release: `build` tags the base and waits for the
draft CI makes for the tag, `sign` asks mckean to sign the draft's
checksums, `verify` checks the signature and each archive's attestation,
and `publish` releases the draft and files the yardr.dev sync bead (the
release flow, `.yardr/flows/type/release.flow`; the script its stages run
is `contrib/release/stage`). With GitHub Actions switched off on the
release repo there is no CI, so `build` builds the release on this
machine instead and `verify` skips the attestations
([below](#with-actions-off)).

```sh
yardr bead create "yardr v1.2.3" -t release -d yardr -b - < notes.md
yardr bead advance <id> --to build
```

The title's last word is the version; the body is the release notes as they
will stand on the GitHub release. The yardmaster shapes the bead in
`backlog`, then advances it to `build`, and from there:

- `build` refuses a bad title, a published release, a tag at another
  commit, a dirty tree, or a checkout that is not the remote's `main`;
  tags the base commit, pushes the tag, and waits for the draft release
  CI makes for the tag, bounded; sets the draft's notes from the bead's
  body and notes the five signing commands on the bead. A CI run that
  concluded anything but success fails the bead with the run's URL.
- `sign` asks mckean whether `checksums.txt.minisig` is uploaded. Run the
  five commands from the build's note, in a directory of their own, then
  Done. Each names the version, so a stale file or a wrong release shows
  itself.
- `verify` downloads the draft, checks the signature against the release
  key, the archives against the checksums, and each archive's
  build-provenance attestation from the release workflow for this tag
  (`gh attestation verify`). `install.sh` is no archive and has no
  attestation: the checksums name it, and it is held to the tag besides, so
  the draft holds the tagged commit's own file, byte for byte, or the bead
  waits in `decide`. A bad signature asks `sign` again.
- `publish` releases the draft, files the yardr.dev sync bead and advances
  it to `new`; the release bead is `published`.

Anything failed or questioned waits in `decide` for a person. To build the
release again, delete the tag and the draft release first, then advance the
bead back to `build`: the pushed tag is what starts CI. The yard's releases
leave the darwin binaries unsigned; a signed build is by hand, below. The
group and the routes are the yard's, and the yardmaster adds them once
([setup.md](setup.md#letting-the-yard-cut-releases)).

`contrib/release/build <version> [out-dir]` builds the four archives and
`checksums.txt` (into `dist/` by default) on any machine with Go: it
cross-compiles without cgo, stamps the version, and runs the binary for the
machine it is on to check the stamp. It copies the tree's `install.sh` beside
them: a file of the release in no archive (footplate refuses an archive member
it does not know) but with a line in `checksums.txt`, so that the signature
covers the installer too (the site's sync takes an `install.sh` only against
that line). It refuses a tree with uncommitted or untracked files.
The darwin binaries it builds are not signed, and it says so, unless it is told
to sign them ([below](#signed-darwin-binaries)). The same holds for
`checksums.txt` ([Signed checksums](#signed-checksums)). With
`SOURCE_DATE_EPOCH` set to seconds since the epoch the build is reproducible:
every archive entry carries that time, and the same inputs make the same
bytes.

`.github/workflows/release.yml` runs when a tag `v*` is pushed: the same
test jobs as `ci.yml`, then that script with `SOURCE_DATE_EPOCH` fixed to
the tagged commit (so a tag rebuilt makes the release's archives again), an
SBOM of the source, build-provenance attestations for the four archives,
and a draft release holding the archives, `checksums.txt` and `install.sh`.
Every action it uses is pinned by full commit SHA. A tag with a pre-release part
(`v1.2.3-rc.1`) makes a pre-release. Started by hand (Actions, "release",
"Run workflow") it is a dry run: the same tests and build, the archives
and the SBOM kept for a week as the run's artifacts, and nothing attested
or published. The workflow runs on Linux and holds no Apple identity, so
the darwin binaries of a draft it made are unsigned. It holds no minisign
key either, so its `checksums.txt` is unsigned until it is signed by hand.

### With Actions off

When GitHub Actions is switched off on the release repo — a private repo
whose minutes ran out — there is no workflow to make the draft, so the flow
builds the release on this machine instead.
`build` asks `gh api repos/<owner>/<repo>/actions/permissions` after
pushing the tag: when it answers `false`, it runs
`contrib/release/build` from the tagged checkout with `SOURCE_DATE_EPOCH`
from the tagged commit, as the workflow sets it, and makes the draft from
the four archives, `checksums.txt` and `install.sh`, with the bead's body as
its notes.
There is no attestation: nothing but this machine vouches for the build,
and the build's note says so. The five signing commands, `sign`, and
`publish` are unchanged; `verify` checks the signature and the checksums
as ever and skips the attestations. A draft already standing is adopted in
both modes, and a release is rebuilt the same way: delete the tag and the
draft, then advance the bead back to `build`. When Actions is on, or the
check cannot run, the flow waits for CI's draft as ever. There is no
setting to force a local build: the repo's Actions state is the switch.

### Signed darwin binaries

On a Mac, with two variables set, the script signs each darwin binary
(`codesign`, with the hardened runtime, a secure timestamp and the
identifier `dev.yardr.yardr`), sends it to Apple's notary service in a zip
and waits for the answer (`xcrun notarytool submit --wait`, minutes as a
rule), and only then packs the archive, so the archive and its line in
`checksums.txt` are of the signed binary. The linux archives are built as
ever.

```sh
YARDR_RELEASE_SIGN_IDENTITY="Developer ID Application: Your Name (TEAMID1234)" \
YARDR_RELEASE_NOTARY_PROFILE=yardr-notary \
  contrib/release/build v1.2.3
```

One variable without the other, or both on a system that is not macOS, is
an error before anything is built. A binary Apple does not accept fails the
build, which prints the notary service's log for the submission. No ticket
is stapled, as a bare binary cannot carry one ([macOS](#macos)).

The setup is done once per Mac, and needs the Xcode command line tools
(`xcode-select --install`) and membership of the Apple Developer Program.

1. A "Developer ID Application" certificate, with its private key, in the
   login keychain. Only the Account Holder of the team can create one: in
   Xcode under Settings, Accounts, the team, Manage Certificates, "+",
   "Developer ID Application"; or at
   <https://developer.apple.com/account/resources/certificates/add> with a
   certificate signing request from Keychain Access. The identity is there
   when this lists it, and the quoted name is the value of
   `YARDR_RELEASE_SIGN_IDENTITY`:

   ```sh
   security find-identity -v -p codesigning
   ```

2. An App Store Connect API key for the notary service: at
   <https://appstoreconnect.apple.com/access/integrations/api>, a team key
   with the role Developer. Download its `AuthKey_<key id>.p8` (offered
   once), and note the key's ID and the issuer ID the page shows.

3. The key, stored in the keychain as a profile whose name is the value of
   `YARDR_RELEASE_NOTARY_PROFILE`. notarytool checks the key with Apple
   before it saves it:

   ```sh
   xcrun notarytool store-credentials yardr-notary \
     --key ~/Downloads/AuthKey_ABCDE12345.p8 \
     --key-id ABCDE12345 \
     --issuer 00000000-0000-0000-0000-000000000000
   ```

   The profile works when this lists the team's submissions (none, at
   first) instead of failing:

   ```sh
   xcrun notarytool history --keychain-profile yardr-notary
   ```

The files of a signed build replace the ones the workflow attached to the
draft, all of them together, as `checksums.txt` is of this build's
archives and installer:

```sh
gh release upload v1.2.3 dist/*.tar.gz dist/checksums.txt dist/install.sh --clobber
```

A signature the draft had is of the checksums this replaces: build with
the minisign key too ([below](#signed-checksums)) and upload
`dist/checksums.txt.minisig` with the rest. A draft whose archives were
replaced by hand is no longer CI's build, so its attestations no longer
verify; `verify` checks them next, and questions the bead.

### Signed checksums

With `YARDR_RELEASE_MINISIGN_KEY` set to the path of a
[minisign](https://jedisct1.github.io/minisign/) secret key, the script
signs `checksums.txt` with it and writes `checksums.txt.minisig` beside it.
The signature covers the checksums, and the checksums cover each archive and
`install.sh`, so one file vouches for the whole release: who has the public
key can tell that the archives and the installer are the ones the key's holder
built. It is what a program that fetches yardr by itself verifies, with the
public key in its source.

```sh
YARDR_RELEASE_MINISIGN_KEY=~/.minisign/yardr-release.key \
  contrib/release/build v1.2.3
```

minisign asks for the key's password on the terminal. A key that is named
and is no readable file, or no `minisign` on PATH (`brew install minisign`,
`apt install minisign`), is an error before anything is built; a key
minisign cannot sign with fails the build, which then leaves no
`checksums.txt`. Without the variable the script says that the checksums are
not signed, and removes a `checksums.txt.minisig` an earlier build left in
the out-dir.

The key pair is made once, and the secret half stays on the machine of
whoever cuts the releases, under a password; it is in no repository and not
on the workflow's runner. `-p` and `-s` say where the two halves go:

```sh
minisign -G -p ~/.minisign/yardr-release.pub -s ~/.minisign/yardr-release.key
```

The public half is one line, which `minisign -G` prints and the `.pub` file
holds. yardr's is `RWSzUPGBbv7MDqH+nnbNmYdOm20UTkuk7eC5e6XuVtYRwZtaEZRlV/WB`
(id `0ECCFE6E81F150B3`), the one `install.sh` and
[From a release archive](#from-a-release-archive) verify with. A new key
is a new footplate and a new `install.sh`, as each verifies with the one it
was built with.

A draft the workflow made has unsigned checksums. They are signed as they
stand, with no new build, and the signature is added to the draft:

```sh
gh release download v1.2.3 --repo yardr-dev/yardr --pattern checksums.txt --clobber
minisign -S -s ~/.minisign/yardr-release.key -m checksums.txt
minisign -V -P RWSzUPGBbv7MDqH+nnbNmYdOm20UTkuk7eC5e6XuVtYRwZtaEZRlV/WB -m checksums.txt
gh release upload v1.2.3 --repo yardr-dev/yardr checksums.txt.minisig --clobber
```

The download's `--clobber` replaces a `checksums.txt` left from an earlier
release, which the download would otherwise refuse to overwrite; the
upload's `--clobber` means a second try replaces the first signature.
Either way, check the signature against the public half before it is
uploaded: the verify line above must print Signature and comment signature
verified.
