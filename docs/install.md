# Installing yardr

yardr is one static binary. There are two ways to get it: a release archive,
which needs no Go, or `go install` from a checkout. Either way it then needs
git, herdr and an agent harness beside it, and `yardr doctor` says which of
those are missing.

## From a release archive

Each release has four archives, `yardr_<version>_<os>_<arch>.tar.gz` for
darwin/arm64, darwin/amd64, linux/amd64 and linux/arm64. An archive holds
`yardr`, the README, and the licence (FSL-1.1-ALv2) as `LICENSE` and
`NOTICE`. `checksums.txt`, with the sha256 of each archive, is
a file of the release beside the archives, not inside them: download it with
the archive.

**The archives need a login while the repository is private.** A release
asset of a private repository is served only to an account that can read the
repository, so a plain `curl` of its address gets a 404. `gh`, logged in as
such an account (`gh auth login`), downloads it:

```sh
version=1.2.3
target=darwin_arm64        # or darwin_amd64, linux_amd64, linux_arm64
gh release download "v$version" --repo yardr-dev/yardr \
  --pattern "yardr_${version}_$target.tar.gz" --pattern checksums.txt

# verify: the line for the archive must say OK
shasum -a 256 --ignore-missing -c checksums.txt      # Linux: sha256sum --ignore-missing -c checksums.txt

# unpack, and put the binary on PATH
tar -xzf "yardr_${version}_$target.tar.gz" yardr
mv yardr ~/.local/bin/     # any directory on your PATH
yardr --version            # the release's tag and revision; yardr version prints the same
```

`checksums.txt` comes from the same release as the archive, so it shows that
the download is whole, not who made it. A release whose checksums were
signed ([Signed checksums](#signed-checksums)) has `checksums.txt.minisig`
beside them, a [minisign](https://jedisct1.github.io/minisign/) signature
that shows who made them, to whoever has the release key's public half.
Download it with the other two (`--pattern checksums.txt.minisig`) and
verify it before the checksums are believed:

```sh
minisign -V -P RWSzUPGBbv7MDqH+nnbNmYdOm20UTkuk7eC5e6XuVtYRwZtaEZRlV/WB -m checksums.txt
```

The key after `-P` is yardr's release key (id `0ECCFE6E81F150B3`), whose
secret half stays with whoever cuts the releases; a release before v0.10.1
may have no signature.

### macOS

A release's darwin binaries are signed with an Apple Developer ID and
notarized by Apple when the release was built on a Mac that has the identity
([Making a release](#making-a-release)), and not otherwise: the workflow
alone builds them unsigned. The binary says which it is:

```sh
codesign -dv --verbose=2 ~/.local/bin/yardr
```

A signed one prints `Authority=Developer ID Application: …` and
`Identifier=dev.yardr.yardr`. An unsigned arm64 binary is signed ad hoc by
the Go linker, exactly like a `go install`ed build (`Signature=adhoc`), and
an unsigned amd64 binary carries no signature at all.

What macOS does with an unsigned binary depends on how it arrived. `gh` and
`curl` do not mark what they download, and the binary runs. A browser marks
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

## With go install

From a checkout, with the Go toolchain `go.mod` names:

```sh
go install .
```

This writes `yardr` to `$(go env GOPATH)/bin`, which has to be on PATH.
`yardr --version`, or `yardr version`, prints its revision (`abcdef123456`,
with `-modified` for a build from a tree with uncommitted changes).

## What yardr doctor still asks for

Neither way installs anything but yardr. Run `yardr doctor`
([doctor.md](doctor.md)): it works before there is a yard and names what is
missing, with where to get it:

- **git**, on PATH;
- **herdr**, on PATH and with its server running: it runs the agents' panes
  ([herdr's server](#herdrs-server));
- **a harness**: an agent herdr starts (claude, codex, opencode, …), on PATH
  and answering its version flag. That is not yet a harness that works:
  `yardr doctor --try` makes one real call to it, which costs a model call
  and says when it is logged out ([doctor.md](doctor.md#--try-one-real-call-to-each-harness)).

A program doctor reports as not on PATH can be installed all the same: over
ssh with a command, and in a service, the shell reads no profile and lacks
what yours adds (`~/.local/bin`, where claude and codex install themselves).
Doctor asks your login shell and names the directory when that finds it.

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

To keep either running from login on, save a service file of your own
([Keep it running after a reboot](#keep-it-running-after-a-reboot)).

`yardr daemon install` installs yardr's daemon, not herdr's server, and
`yardr up` starts herdr's server without installing anything: after a reboot
it is `yardr up` again, or the service file.

## Keep it running after a reboot

Three things keep a yard going, and a reboot stops all three. Two are
yardr's, and one command each keeps them running from login on: a launchd
agent on macOS, a systemd user unit on Linux
([configuration.md](configuration.md#login-services)).

```sh
yardr daemon install     # the daemon: dispatches beads, starts agents
yardr web install        # the web view at http://127.0.0.1:8791/
```

`yardr status` names the missing one: `login service: not installed (yardr
daemon install)`, and the like for the web view.

The third is herdr's server, which starts the agents' panes. herdr's own
help offers no command that installs it at login, and yardr installs
nothing for herdr: save a service file of your own. One server serves
every yard on the machine, so one service is enough, and yardr cannot
tell whether one is installed: `yardr doctor` says only that it installs
nothing.

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

The plist is an example, which yardr neither writes nor tests; herdr's own
documentation is what holds for its server. The unit is what
`contrib/server-yard/server-yard` writes (the two values quoted as systemd
needs), and its test holds it.

- The path of herdr is absolute in both: a service reads no profile, so
  `herdr` alone is not found. `command -v herdr` in your login shell
  prints it.
- The PATH is the full one of that shell: the agents' panes inherit the
  server's environment, and what launchd and systemd give it without one
  is minimal (`~/.local/bin`, where harnesses install themselves, is not
  in it). If a harness is then not found in a pane, the PATH is what to
  fix.
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

One script does these steps, and a second run of it changes nothing:
`contrib/server-yard/server-yard --harness <kind> --operator <name>`
([its README](../contrib/server-yard/README.md)). What follows is what it
does, step by step.

1. **Put the programs where every shell finds them.** yardr, herdr and the
   harness have to be on the PATH of three shells: yours, a shell without a
   terminal (`ssh host yardr doctor`), and a systemd user service. The last
   two read no profile, and on many systems `~/.local/bin` is on PATH only
   through `~/.profile`. Either install into a directory all three have
   (`/usr/local/bin`), or keep `~/.local/bin` and name it where it is
   missing: `yardr doctor` says which program your login shell finds that
   the shell it runs in does not, and in which directory.

2. **Run herdr's server as a service**, with the systemd user unit under
   [Keep it running after a reboot](#keep-it-running-after-a-reboot).
   Nothing starts an agent before it runs.

3. **Let your services run with nobody logged in:**
   `loginctl enable-linger "$USER"`. Without it a systemd user unit ends
   with your last ssh session
   ([configuration.md](configuration.md#login-services)).

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
   yardr doctor
   ```

   `yardr daemon install` writes the PATH of the shell it runs in into the
   unit, and names the programs that PATH lacks. Run it again after the PATH
   changes ([configuration.md](configuration.md#login-services)).

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

This part is written from one such install (v0.1.0, Ubuntu, 5 Oct 2026) and
from herdr's documentation. The script is tested with stand-ins for herdr
and the harness (its README); yardr's tests run on no machine without a
desktop.

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

Install the newer build over the old one, by either way: unpack the newer
archive and move `yardr` to where the old one is, or `go install` again. The
server and daemon notice the new file and replace themselves
([configuration.md](configuration.md#upgrading)). The two ways mix: a release
archive replaces a `go install`ed build of an earlier commit, and the other
way round.

## Making a release

`contrib/release/build <version> [out-dir]` builds the four archives and
`checksums.txt` (into `dist/` by default) on any machine with Go: it
cross-compiles without cgo, stamps the version, and runs the binary for the
machine it is on to check the stamp. It refuses a tree with uncommitted or
untracked files. The darwin binaries it builds are not signed, and it says
so, unless it is told to sign them ([below](#signed-darwin-binaries)). The
same holds for `checksums.txt` ([Signed checksums](#signed-checksums)).

`.github/workflows/release.yml` runs the depot's tests and then that script when
a tag `v*` is pushed, and creates the GitHub release with the files; a tag
with a pre-release part (`v1.2.3-rc.1`) makes a pre-release. Started by hand
(Actions, "release", "Run workflow") it is a dry run: the same tests and
build, the archives kept for a week as the run's `dist` artifact, and no
release. The workflow runs on Linux and holds no Apple identity, so the
darwin binaries of a release it made are unsigned. It holds no minisign key
either, so its `checksums.txt` is unsigned until it is signed by hand.

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
release, all of them together, as `checksums.txt` is of this build's
archives:

```sh
gh release upload v1.2.3 dist/*.tar.gz dist/checksums.txt --clobber
```

A signature the release had is of the checksums this replaces: build with
the minisign key too ([below](#signed-checksums)) and upload
`dist/checksums.txt.minisig` with the rest.

### Signed checksums

With `YARDR_RELEASE_MINISIGN_KEY` set to the path of a
[minisign](https://jedisct1.github.io/minisign/) secret key, the script
signs `checksums.txt` with it and writes `checksums.txt.minisig` beside it.
The signature covers the checksums, and the checksums cover each archive, so
one file vouches for the whole release: who has the public key can tell that
the archives are the ones the key's holder built. It is what a program
that fetches yardr by itself verifies, with the public key in its source.

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
(id `0ECCFE6E81F150B3`), the one
[From a release archive](#from-a-release-archive) verifies with. A new key
is a new footplate, as footplate verifies with the one it was built with.

A release the workflow made has unsigned checksums. They are signed as they
stand, with no new build, and the signature is added to the release:

```sh
gh release download v1.2.3 --repo yardr-dev/yardr --pattern checksums.txt
minisign -S -s ~/.minisign/yardr-release.key -m checksums.txt
gh release upload v1.2.3 --repo yardr-dev/yardr checksums.txt.minisig
```

Either way, check the signature against the public half before it is
uploaded (`Signature and comment signature verified`):

```sh
minisign -V -p ~/.minisign/yardr-release.pub -m checksums.txt
```
