#!/bin/sh
# install.sh: install yardr into ~/.local/bin, on macOS and Linux. It is the
# one way yardr is installed and the only definition of where it goes
# (docs/install.md).
#
#   curl -fsSL https://yardr.dev/install.sh | sh
#   ./install.sh [--from-source]
#
# With no flag it installs the latest release. latest.json, in the releases
# directory of yardr.dev, names the version and the paths; the script downloads
# checksums.txt, checksums.txt.minisig and this system's archive from yardr.dev
# and from nowhere else, and writes nothing to ~/.local/bin until its checks
# have passed:
#
#   - where minisign is on PATH, the signature on checksums.txt must verify
#     against yardr's release key, so that the checksums are believed;
#   - where it is not, the script says that only the checksum was checked,
#     which shows the download is whole and not who made it;
#   - the archive must match its line in checksums.txt.
#
# docs/install.md ("From a release archive") does the same by hand.
#
# --from-source builds the checkout the script sits in with go build, so it
# needs Go, and installs that: nothing is downloaded. It is a contributor's
# road.
#
# Both write the program to a temporary file in ~/.local/bin, run it with
# --version, and rename it into place. A yardr that is running is replaced
# whole (the server and daemon notice the new file), and a program that cannot
# run replaces nothing. Of the archive only `yardr` is unpacked. Installing over
# a newer yardr is allowed. When ~/.local/bin is not on PATH the script says
# the line to add; it changes no shell profile. When herdr is not on PATH it
# prints herdr's own installer as a line to run, and says that yardr also needs
# an agent; it runs and installs nothing of that.
#
# POSIX sh. A release needs curl, tar, awk and sha256sum or shasum. The
# script is functions and its last line calls main: the shell has read all of
# it before it runs any, so a download cut short runs nothing, and no command
# in it can read the rest of the script off the stdin of `curl ... | sh`.
set -eu
# What awk reads is ASCII; a locale's multibyte rules only make a page that is
# not (a proxy's error page, say) fail in odd ways.
LC_ALL=C
export LC_ALL

# yardr's release key, the public half of the pair that signs checksums.txt
# (id 0ECCFE6E81F150B3); docs/install.md names the same. A new key is a new
# installer.
pubkey=RWSzUPGBbv7MDqH+nnbNmYdOm20UTkuk7eC5e6XuVtYRwZtaEZRlV/WB

work=
partial=
cleanup() {
  [ -z "$work" ] || rm -rf "$work"
  [ -z "$partial" ] || rm -f "$partial"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

say() { printf 'install.sh: %s\n' "$*"; }
die() {
  printf 'install.sh: %s\n' "$*" >&2
  exit 1
}

usage() {
  echo "usage: install.sh [--from-source]" >&2
  exit 2
}

help() {
  cat <<'EOF'
usage: install.sh [--from-source]

Installs yardr into ~/.local/bin. With no flag it installs the latest release
from yardr.dev, after checking its checksum, and its signature where minisign
is on PATH. --from-source builds the checkout this script is in with go build
instead, and needs Go.
EOF
}

# need <program>: dies unless it is on PATH.
need() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is not on PATH, and the install needs it"
}

# field <file> <key> [<object>]: the string value of "key" in a JSON file, or
# of the first "key" inside the object named <object>. latest.json is small and
# written by one script, so this reads what the script needs and not all of
# JSON: the text is joined into one line, an object ends at the first "}" after
# its "{" (no value here holds one), and the first match wins. The formatting
# of the file is no part of it.
field() {
  awk -v key="$2" -v obj="${3:-}" '
    { s = s $0 }
    END {
      if (obj != "") {
        if (!match(s, "\"" obj "\"[ \t]*:[ \t]*[{][^}]*[}]")) exit 1
        s = substr(s, RSTART, RLENGTH)
      }
      if (!match(s, "\"" key "\"[ \t]*:[ \t]*\"[^\"]*\"")) exit 1
      s = substr(s, RSTART, RLENGTH)
      sub(/^[^:]*:[ \t]*"/, "", s)
      sub(/"$/, "", s)
      print s
    }' "$1"
}

# plain <name> <path>: dies unless the path latest.json gave for <name> is a
# plain one under /releases/. The path is put behind the host, and an @ or a
# second slash in front of it would name another one; the value is not printed,
# as it is the site's text and may hold what a terminal obeys.
plain() {
  case $2 in
  /releases/*..* | /releases/*[!A-Za-z0-9._/-]*) ;;
  /releases/*) return 0 ;;
  esac
  die "latest.json's $1 is not a plain path under /releases/; nothing was installed"
}

# fetch <path> <file>: asks yardr.dev for one https response, without reading
# curl's config or following redirects, and keeps only a 2xx response as the
# file.
fetch() {
  status=$(curl --disable --fail --silent --show-error --proto '=https' --tlsv1.2 \
    --output "$2" --write-out '%{http_code}' "https://yardr.dev$1") ||
    die "downloading https://yardr.dev$1 failed; nothing was installed"
  case $status in
  2[0-9][0-9]) ;;
  *) die "downloading https://yardr.dev$1 returned HTTP ${status:-no status}; nothing was installed" ;;
  esac
}

# sha256_of <file>: its sha256, from sha256sum where there is one (Linux) and
# from shasum where not (macOS).
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{ print $1 }'
  else
    shasum -a 256 "$1" | awk '{ print $1 }'
  fi
}

# release downloads the latest release into $work and checks it, leaving the
# archive's name in $archive, the version in $version and in $signed whether
# the signature was checked.
release() {
  need curl
  need tar
  need awk
  command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 ||
    die "neither sha256sum nor shasum is on PATH, so a download could not be checked"

  case $(uname -s) in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "there is no yardr release for $(uname -s); there are releases for macOS and Linux" ;;
  esac
  case $(uname -m) in
  arm64 | aarch64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) die "there is no yardr release for $(uname -m); there are releases for arm64 and amd64" ;;
  esac
  # A shell started under Rosetta on an Apple silicon Mac says x86_64, though
  # the machine runs the arm64 build natively; sysctl does not take the shell's
  # word for it (it prints nothing on an Intel Mac, which has no such key).
  if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
    [ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" = 1 ]; then
    arch=arm64
  fi
  target=${os}_$arch

  work=$(mktemp -d "${TMPDIR:-/tmp}/yardr-install.XXXXXX") ||
    die "cannot make a temporary directory in ${TMPDIR:-/tmp}"
  fetch /releases/latest.json "$work/latest.json"
  version=$(field "$work/latest.json" version) || version=
  case $version in
  "" | *[!A-Za-z0-9._-]*) die "latest.json names no usable version; nothing was installed" ;;
  esac
  sums=$(field "$work/latest.json" checksums) || sums=
  sig=$(field "$work/latest.json" signature) || sig=
  # Not "path": zsh ties that name to PATH, and a script sent to zsh by
  # mistake should not lose its PATH to a variable.
  archive_path=$(field "$work/latest.json" path "$target") ||
    die "latest.json lists no yardr $version for $target; nothing was installed"
  plain checksums "$sums"
  plain signature "$sig"
  plain "$target archive" "$archive_path"
  archive=${archive_path##*/}
  say "installing yardr $version for $target"

  # The checksums and what vouches for them come before the archive, so a
  # release that does not check costs two small files and not the archive.
  fetch "$sums" "$work/checksums.txt"
  fetch "$sig" "$work/checksums.txt.minisig"
  signed=
  if command -v minisign >/dev/null 2>&1; then
    # Its own words on a bad signature go to stderr, which is left open.
    minisign -V -P "$pubkey" -x "$work/checksums.txt.minisig" -m "$work/checksums.txt" >/dev/null ||
      die "the signature on checksums.txt does not verify against yardr's release key (id 0ECCFE6E81F150B3); nothing was installed"
    signed=1
    say "the signature on checksums.txt verifies against yardr's release key"
  fi

  say "downloading $archive"
  fetch "$archive_path" "$work/$archive"
  want=$(awk -v name="$archive" '$2 == name || $2 == "*" name { print $1; exit }' "$work/checksums.txt")
  case $want in
  "" | *[!0-9a-f]*) die "checksums.txt has no sha256 for $archive; nothing was installed" ;;
  esac
  [ "${#want}" -eq 64 ] || die "checksums.txt has no sha256 for $archive; nothing was installed"
  [ "$(sha256_of "$work/$archive")" = "$want" ] ||
    die "$archive does not match its sha256 in checksums.txt; nothing was installed"
  say "$archive matches its sha256 in checksums.txt"
}

# checkout finds the checkout --from-source builds, in $src: the one the script
# sits in. A go.mod of another module there would have go build make some other
# program and install it as yardr.
checkout() {
  case $0 in
  */*) src=${0%/*} ;;
  *) src=. ;;
  esac
  src=$(cd "$src" && pwd) || die "cannot read the directory $0 is in"
  if [ ! -f "$src/go.mod" ] || ! grep -q '^module yardr.dev/yardr$' "$src/go.mod"; then
    die "--from-source builds the checkout this script sits in, and $src is none (it has no go.mod for yardr.dev/yardr); run it from a checkout of yardr"
  fi
  command -v go >/dev/null 2>&1 ||
    die "--from-source needs Go on PATH, the version go.mod names (https://go.dev/doc/install)"
}

main() {
  from_source=
  while [ $# -gt 0 ]; do
    case $1 in
    --from-source) from_source=1 ;;
    -h | --help)
      help
      exit 0
      ;;
    *) usage ;;
    esac
    shift
  done
  [ -n "${HOME:-}" ] || die "HOME is not set"
  bin=$HOME/.local/bin

  if [ -n "$from_source" ]; then
    checkout
  else
    release
  fi

  mkdir -p "$bin" || die "cannot make $bin"
  partial=$(mktemp "$bin/.yardr.XXXXXX") || die "cannot write in $bin"
  if [ -n "$from_source" ]; then
    say "building $src with go build"
    (cd "$src" && go build -o "$partial" .) || die "go build failed; nothing was installed"
  else
    tar -xzOf "$work/$archive" yardr >"$partial" ||
      die "$archive holds no yardr to unpack; nothing was installed"
  fi
  chmod 755 "$partial"
  built=$("$partial" --version </dev/null 2>&1) ||
    die "the yardr that was made does not run here (it said: $built); nothing was installed"
  [ ! -d "$bin/yardr" ] || die "$bin/yardr is a directory; nothing was installed"
  mv -f "$partial" "$bin/yardr"
  partial=
  say "installed ~/.local/bin/yardr: $built"

  if [ -z "$from_source" ] && [ -z "$signed" ]; then
    say "minisign is not on PATH, so only the checksum was checked: it shows the download is whole, not who made it."
    say "To check the signature too, install minisign (https://jedisct1.github.io/minisign/ lists the ways; brew install minisign on macOS, apt install minisign on Debian) and run this again."
  fi
  found=$(command -v yardr 2>/dev/null || true)
  case ":$PATH:" in
  *":$bin:"*)
    case $found in
    "" | "$bin/yardr") ;;
    *) say "note: yardr on your PATH is $found, which comes before ~/.local/bin, so that one runs and not the one just installed; remove it, or put ~/.local/bin first on PATH" ;;
    esac
    ;;
  *)
    say "yardr is in ~/.local/bin, which is not on your PATH. Add this line to your shell's startup file (~/.zshrc, ~/.bashrc or ~/.profile), then open a new terminal:"
    # shellcheck disable=SC2016 # the line is for a profile, where $HOME expands
    printf '\n    export PATH="$HOME/.local/bin:$PATH"\n\n'
    [ -z "$found" ] || say "note: until then yardr runs $found, not the one just installed"
    ;;
  esac

  # What yardr still needs beside itself. This only looks, with command -v:
  # herdr's installer is another project's script, and running it is for the
  # person to do.
  if ! command -v herdr >/dev/null 2>&1; then
    say "herdr is not on PATH, and yardr starts every agent through it. Get it with this command (more: https://herdr.dev):"
    printf '\n    curl -fsSL https://herdr.dev/install.sh | sh\n\n'
    say "yardr also needs an agent such as claude or codex."
  fi
}

main "$@"
