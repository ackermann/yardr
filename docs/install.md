# Installing yardr

yardr is one static binary. There are two ways to get it: a release archive,
which needs no Go, or `go install` from a checkout. Either way it then needs
git, herdr and an agent harness beside it, and `yardr doctor` says which of
those are missing.

## From a release archive

Each release has four archives, `yardr_<version>_<os>_<arch>.tar.gz` for
darwin/arm64, darwin/amd64, linux/amd64 and linux/arm64, and a
`checksums.txt` with the sha256 of each. An archive holds `yardr` and the
README.

**The archives need a login while the repository is private.** A release
asset of a private repository is served only to an account that can read the
repository, so a plain `curl` of its address gets a 404. `gh`, logged in as
such an account (`gh auth login`), downloads it:

```sh
version=1.2.3
target=darwin_arm64        # or darwin_amd64, linux_amd64, linux_arm64
gh release download "v$version" --repo McKean/yardr \
  --pattern "yardr_${version}_$target.tar.gz" --pattern checksums.txt

# verify: the line for the archive must say OK
shasum -a 256 --ignore-missing -c checksums.txt      # Linux: sha256sum --ignore-missing -c checksums.txt

# unpack, and put the binary on PATH
tar -xzf "yardr_${version}_$target.tar.gz" yardr
mv yardr ~/.local/bin/     # any directory on your PATH
yardr --version            # v1.2.3 (<revision>); yardr version prints the same
```

`checksums.txt` comes from the same release as the archive, so it shows that
the download is whole, not who made it: the archives are not signed.

### macOS

The binaries are not signed with an Apple developer identity and not
notarised. The arm64 binary is signed ad hoc by the Go linker, exactly like
a `go install`ed build (`codesign -dv yardr` says `Signature=adhoc`); the
amd64 binary carries no signature at all.

What macOS does with such a binary depends on how it arrived. `gh` and
`curl` do not mark what they download, and the binary runs. A browser marks
the archive as quarantined (`com.apple.quarantine`), and an unpacker can
pass the mark on to `yardr` (Finder's does); Gatekeeper then refuses to run
it, as software Apple cannot check. Clear the mark:

```sh
xattr -d com.apple.quarantine ~/.local/bin/yardr
```

`xattr ~/.local/bin/yardr` lists the marks a file has; no
`com.apple.quarantine` in it means there is nothing to clear.

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
- **herdr**, on PATH: it runs the agents' panes;
- **a harness**: an agent herdr starts (claude, codex, opencode, …), on PATH
  and answering its version flag.

Then run `yardr` for the onboarding ([the README](../README.md#quick-start)).

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
untracked files.

`.github/workflows/release.yml` runs the merge gate and then that script when
a tag `v*` is pushed, and creates the GitHub release with the files; a tag
with a pre-release part (`v1.2.3-rc.1`) makes a pre-release. Started by hand
(Actions, "release", "Run workflow") it is a dry run: the same gate and
build, the archives kept for a week as the run's `dist` artifact, and no
release.
